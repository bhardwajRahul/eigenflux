package consumer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"eigenflux_server/kitex_gen/eigenflux/pm"
	"eigenflux_server/kitex_gen/eigenflux/pm/pmservice"
	"eigenflux_server/pipeline/llm"
	"eigenflux_server/pipeline/official"
	"eigenflux_server/pkg/config"
	"eigenflux_server/pkg/db"
	"eigenflux_server/pkg/mq"
	"eigenflux_server/pkg/rpcx"
	pmdal "eigenflux_server/rpc/pm/dal"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/pkg/kerrors"
	etcd "github.com/kitex-contrib/registry-etcd"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestOfficialWelcomeE2E drives the welcome consumer's handle() against the live
// PM service: it must friend the new agent and deliver exactly one welcome PM.
// Requires the local stack (PM RPC + etcd + PG + Redis) and a provisioned
// official account. Only missing prerequisites or an unreachable service skip
// the test; failures after the welcome begins must fail it.
func TestOfficialWelcomeE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("requires the local PM, PostgreSQL, and Redis integration stack")
	}
	cfg := config.Load()
	gdb, err := gorm.Open(postgres.Open(cfg.PgDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true,
	})
	require.NoError(t, err)
	sqlDB, err := gdb.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(probeCtx); err != nil {
		if welcomePrerequisiteUnavailable(err) {
			t.Skipf("local PostgreSQL is unreachable before welcome: %v", err)
		}
		t.Fatalf("PostgreSQL preflight: %v", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	if err := rdb.Ping(probeCtx).Err(); err != nil {
		if welcomePrerequisiteUnavailable(err) {
			t.Skipf("local Redis is unreachable before welcome: %v", err)
		}
		t.Fatalf("Redis preflight: %v", err)
	}
	previousDB, previousRedis := db.DB, mq.RDB
	db.DB, mq.RDB = gdb, rdb
	t.Cleanup(func() { db.DB, mq.RDB = previousDB, previousRedis })

	var officialID int64
	if err := db.DB.Raw(
		"SELECT agent_id FROM agents WHERE email = ? AND is_official", cfg.OfficialAgentEmail,
	).Scan(&officialID).Error; err != nil {
		t.Fatalf("look up official account: %v", err)
	}
	if officialID == 0 {
		t.Skip("official account not provisioned locally")
	}

	// Probe the real RPC before creating friendship or sending anything. A
	// reachable service returning a business error is a failure, not a skip.
	var pmClient pmservice.Client
	if addr := os.Getenv("PM_DIRECT_ADDR"); addr != "" {
		pmClient, err = pmservice.NewClient("PMService", client.WithHostPorts(addr), client.WithRPCTimeout(5*time.Second))
	} else {
		resolver, rerr := etcd.NewEtcdResolver([]string{cfg.EtcdAddr})
		require.NoError(t, rerr)
		pmClient, err = pmservice.NewClient("PMService", rpcx.ClientOptions(resolver)...)
	}
	require.NoError(t, err)
	pmProbeCtx, pmCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pmCancel()
	probe, err := pmClient.ListFriends(pmProbeCtx, &pm.ListFriendsReq{AgentId: officialID})
	if welcomePrerequisiteUnavailable(err) {
		t.Skipf("local PM service is unavailable before welcome: %v", err)
	}
	require.NoError(t, err, "PM preflight")
	require.NotNil(t, probe)
	require.NotNil(t, probe.BaseResp)
	require.Zero(t, probe.BaseResp.Code, "PM preflight rejected: %s", probe.BaseResp.Msg)

	const userID int64 = 9_100_000_000_000_000_077
	now := time.Now().UnixMilli()

	lo, hi := officialID, userID
	if lo > hi {
		lo, hi = hi, lo
	}
	cleanup := func() {
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{"DELETE FROM private_messages WHERE sender_id IN (?,?) AND receiver_id IN (?,?)", []any{officialID, userID, officialID, userID}},
			{"DELETE FROM conversations WHERE participant_a IN (?,?) AND participant_b IN (?,?)", []any{officialID, userID, officialID, userID}},
			{"DELETE FROM user_relations WHERE from_uid = ? OR to_uid = ?", []any{userID, userID}},
			{"DELETE FROM friend_requests WHERE from_uid = ? OR to_uid = ?", []any{userID, userID}},
			{"DELETE FROM agents WHERE agent_id = ?", []any{userID}},
		} {
			if err := db.DB.Exec(statement.sql, statement.args...).Error; err != nil {
				t.Errorf("clean up welcome fixture: %v", err)
			}
		}
		// Clear PM-service Redis caches so a re-run is not poisoned by a stale
		// conversation mapping / friend set pointing at deleted rows.
		if err := mq.RDB.Del(context.Background(),
			officialWelcomedKey(userID),
			fmt.Sprintf("pm:convmap:%d:%d:0", lo, hi),
			fmt.Sprintf("friend:%d", officialID),
			fmt.Sprintf("friend:%d", userID),
			fmt.Sprintf("pm:fetch:%d", userID),
		).Err(); err != nil {
			t.Errorf("clean up welcome Redis keys: %v", err)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	if t.Failed() {
		t.FailNow()
	}

	// A new, profile-complete agent.
	if err := db.DB.Exec(
		`INSERT INTO agents (agent_id, short_id, email, agent_name, bio, created_at, updated_at, profile_completed_at, is_official)
		 VALUES (?, 'WelCm', ?, ?, ?, ?, ?, ?, false)`,
		userID, "welcome-e2e@test.com", "WelcomeE2E", "test", now, now, now,
	).Error; err != nil {
		t.Fatalf("insert test user: %v", err)
	}

	// Exercise delivery with the supported static-message fallback. An empty
	// registry fails rendering before any external LLM request is attempted.
	prompts := &llm.PromptRegistry{}
	sender := official.NewSender(cfg, pmClient, llm.NewClient(cfg, prompts), prompts)
	c := &OfficialWelcomeConsumer{
		sender:         sender,
		welcomeMessage: cfg.OfficialWelcomeMessage,
		officialEmail:  cfg.OfficialAgentEmail,
	}

	res := c.handle(context.Background(), "0-0", map[string]any{"agent_id": strconv.FormatInt(userID, 10)})
	if res != HandleSuccess {
		t.Fatalf("handle result = %v, want HandleSuccess", res)
	}

	// Friendship is created in PG before the PM hop, so it must always hold.
	friend, ferr := pmdal.IsFriend(db.DB, officialID, userID)
	if ferr != nil {
		t.Fatalf("IsFriend: %v", ferr)
	}
	if !friend {
		t.Fatal("expected official and user to be friends after welcome")
	}

	var msgs int64
	if err := db.DB.Raw(
		"SELECT count(*) FROM private_messages WHERE sender_id = ? AND receiver_id = ?",
		officialID, userID,
	).Scan(&msgs).Error; err != nil {
		t.Fatalf("count welcome messages: %v", err)
	}
	if msgs != 1 {
		t.Fatalf("welcome PM count = %d, want 1", msgs)
	}
}

func welcomePrerequisiteUnavailable(err error) bool {
	var dialErr *net.OpError
	return (errors.As(err, &dialErr) && dialErr.Op == "dial") ||
		errors.Is(err, kerrors.ErrGetConnection) ||
		errors.Is(err, kerrors.ErrNoInstance) ||
		errors.Is(err, kerrors.ErrNoMoreInstance)
}

func TestWelcomePrerequisiteUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"ready", nil, false},
		{"dial_failure", fmt.Errorf("connect: %w", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}), true},
		{"rpc_connection_failure", kerrors.ErrGetConnection, true},
		{"rpc_no_instance", kerrors.ErrNoInstance, true},
		{"rpc_no_more_instances", kerrors.ErrNoMoreInstance, true},
		{"business_rejection", kerrors.ErrBiz.WithCause(errors.New("not friends")), false},
		{"rpc_timeout", kerrors.ErrRPCTimeout, false},
		{"database_query_failure", errors.New("missing relation private_messages"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, welcomePrerequisiteUnavailable(tc.err))
		})
	}
}
