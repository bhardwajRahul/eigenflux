// Command test_account_reset removes fixed-OTP test accounts so the same emails
// register as brand-new agents on their next login. It is a dry-run plan by
// default; writes require --apply. Only addresses matching a full-address
// OFFICIAL_TEST_EMAIL_SUFFIXES pattern are accepted.
//
//	go run ./scripts/test_account_reset --emails=kairui3@pgc.eigenflux.one
//	go run ./scripts/test_account_reset --emails=kairui3@pgc.eigenflux.one,kairui4@pgc.eigenflux.one --apply
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"time"

	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/es"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/testaccountreset"

	_ "github.com/lib/pq"
)

func main() {
	emailsFlag := flag.String("emails", "", "comma-separated test-account emails to reset")
	apply := flag.Bool("apply", false, "delete the accounts; default is a read-only plan")
	flag.Parse()

	var emails []string
	for _, e := range strings.Split(*emailsFlag, ",") {
		if e = testaccountreset.NormalizeEmail(e); e != "" {
			emails = append(emails, e)
		}
	}
	if len(emails) == 0 {
		log.Fatal("--emails is required")
	}

	cfg := config.Load()
	// Refuse the whole batch before touching anything if one address is not a test account.
	for _, email := range emails {
		if !testaccountreset.Allowed(email, cfg.OfficialTestEmailSuffixes) {
			log.Fatalf("%s: %v", email, testaccountreset.ErrNotTestAccount)
		}
	}

	db, err := sql.Open("postgres", cfg.PgDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	mq.Init(cfg.RedisAddr, cfg.RedisPassword)
	if err := es.InitClient(); err != nil {
		log.Fatalf("elasticsearch: %v", err)
	}

	for _, email := range emails {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		err := reset(ctx, db, cfg, email, *apply)
		cancel()
		if err != nil {
			log.Fatalf("%s: %v", email, err)
		}
	}
	if !*apply {
		log.Println("no data changed; pass --apply to execute")
	}
}

func reset(ctx context.Context, db *sql.DB, cfg *config.Config, email string, apply bool) error {
	plan, err := testaccountreset.ResetPostgres(ctx, db, email, cfg.OfficialTestEmailSuffixes, false)
	if err != nil {
		return err
	}
	verb := "would delete"
	if apply && len(plan.Blocked) == 0 {
		verb = "deleting"
	}
	if plan.AgentID == 0 {
		log.Printf("%s: no agent row; clearing login leftovers only", email)
	} else {
		log.Printf("%s: agent_id=%d", email, plan.AgentID)
	}
	for _, c := range plan.Deleted {
		if c.Rows > 0 {
			log.Printf("  %s %d from %s", verb, c.Rows, c.Table)
		}
	}
	for _, c := range plan.Cascaded {
		log.Printf("  %s %d from %s (cascade)", verb, c.Rows, c.Table)
	}
	for _, c := range plan.LeftInPlace {
		log.Printf("  WARNING: leaving %d rows in %s that point at this agent (other agents it invited)", c.Rows, c.Table)
	}
	for _, c := range plan.Blocked {
		log.Printf("  BLOCKED: %d rows in %s", c.Rows, c.Table)
	}
	if len(plan.Blocked) > 0 {
		if apply {
			return testaccountreset.ErrHasTrades
		}
		log.Printf("  --apply will refuse this account: %v", testaccountreset.ErrHasTrades)
	}

	// Elasticsearch and Redis go first: if either fails nothing in PostgreSQL has
	// changed, the agent id is still resolvable, and the same command can be rerun.
	if plan.AgentID != 0 {
		n, err := items(ctx, plan.AgentID, apply)
		if err != nil {
			return fmt.Errorf("elasticsearch (postgres untouched, safe to rerun): %w", err)
		}
		log.Printf("  %s %d docs from %s (author_agent_id)", verb, n, es.ReadIndexPattern)
	}
	keys := testaccountreset.RedisKeys(plan, cfg.RecallRedisNamespace)
	if !apply {
		n, err := mq.RDB.Exists(ctx, keys...).Result()
		if err != nil {
			return fmt.Errorf("redis: %w", err)
		}
		log.Printf("  would delete %d of %d redis keys", n, len(keys))
		return nil
	}
	n, err := mq.RDB.Del(ctx, keys...).Result()
	if err != nil {
		return fmt.Errorf("redis (postgres untouched, safe to rerun): %w", err)
	}
	if plan.AgentID != 0 {
		field := strconv.FormatInt(plan.AgentID, 10)
		for _, hash := range testaccountreset.RedisHashes {
			if err := mq.RDB.HDel(ctx, hash, field).Err(); err != nil {
				return fmt.Errorf("redis %s (postgres untouched, safe to rerun): %w", hash, err)
			}
		}
	}
	log.Printf("  deleted %d redis keys", n)

	report, err := testaccountreset.ResetPostgres(ctx, db, email, cfg.OfficialTestEmailSuffixes, true)
	if err != nil {
		return fmt.Errorf("postgres rolled back, safe to rerun: %w", err)
	}
	var rows int64
	for _, c := range report.Deleted {
		rows += c.Rows
	}
	log.Printf("  postgres committed: %d rows deleted directly, the rest by cascade", rows)
	return nil
}

// items counts, or with apply deletes, the agent's broadcasts in the items indices.
func items(ctx context.Context, agentID int64, apply bool) (int64, error) {
	body, _ := json.Marshal(map[string]any{"query": map[string]any{"term": map[string]any{"author_agent_id": agentID}}})
	var (
		status int
		raw    []byte
		field  = "count"
	)
	if apply {
		field = "deleted"
		res, err := es.Client.DeleteByQuery([]string{es.ReadIndexPattern}, bytes.NewReader(body),
			es.Client.DeleteByQuery.WithContext(ctx), es.Client.DeleteByQuery.WithRefresh(true),
			es.Client.DeleteByQuery.WithConflicts("proceed"))
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		status, raw = res.StatusCode, readAll(res.Body)
	} else {
		res, err := es.Client.Count(es.Client.Count.WithContext(ctx),
			es.Client.Count.WithIndex(es.ReadIndexPattern), es.Client.Count.WithBody(bytes.NewReader(body)))
		if err != nil {
			return 0, err
		}
		defer res.Body.Close()
		status, raw = res.StatusCode, readAll(res.Body)
	}
	if status >= 300 {
		return 0, fmt.Errorf("status %d: %s", status, raw)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return 0, err
	}
	if failures, _ := parsed["failures"].([]any); len(failures) > 0 || parsed["timed_out"] == true {
		return 0, fmt.Errorf("incomplete delete: %s", raw)
	}
	if conflicts, _ := parsed["version_conflicts"].(float64); conflicts > 0 {
		return 0, fmt.Errorf("%v version conflicts; rerun", conflicts)
	}
	n, _ := parsed[field].(float64)
	return int64(n), nil
}

func readAll(r io.Reader) []byte {
	raw, _ := io.ReadAll(r)
	return raw
}
