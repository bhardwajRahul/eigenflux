package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
)

func TestPostgresCommunicationAnchorContinuationsAndSearchPreview(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for anchor PostgreSQL contracts")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	t.Cleanup(func() { tx.Rollback() })
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if err := tx.Exec(query, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	base, now := time.Now().UnixNano(), time.Now().UnixMilli()
	viewerID, peerID, convID := base, base+1, base+100
	for _, id := range []int64{viewerID, peerID} {
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO agents (agent_id, short_id, email, agent_name, bio, created_at, updated_at)
            VALUES (?, ?, ?, 'Anchor Peer', '', ?, ?)`, id, shortID, fmt.Sprintf("anchor-%d@example.test", id), now, now)
	}
	exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id,
        last_sender_id, origin_type, origin_id, msg_count, status, updated_at, topic_status)
        VALUES (?, ?, ?, ?, ?, 'friend', 0, 100, 0, ?, 1)`, convID, viewerID, peerID, viewerID, peerID, now)
	for offset := int64(1); offset <= 100; offset++ {
		exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
            VALUES (?, ?, ?, ?, 'context message', true, ?)`, base+1000+offset, convID, peerID, viewerID, now+offset)
	}
	svc := &Service{db: tx}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/conversations/:conv_id/messages", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", viewerID)
		svc.listCommunicationMessages(ctx, c)
	})
	h.GET("/search", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", viewerID)
		svc.searchCommunicationMessages(ctx, c)
	})
	request := func(t *testing.T, path string) map[string]interface{} {
		t.Helper()
		status, payload, _ := performJSON(t, h, http.MethodGet, path, nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d payload=%#v", status, payload)
		}
		return responseData(t, payload)
	}
	historyPath := fmt.Sprintf("/conversations/%d/messages?", convID)
	messageIDs := func(t *testing.T, data map[string]interface{}) []int64 {
		t.Helper()
		ids := []int64{}
		for _, raw := range data["messages"].([]interface{}) {
			ids = append(ids, mustParseInt64(t, raw.(map[string]interface{})["msg_id"].(string))-base-1000)
		}
		if len(ids) == 0 {
			t.Fatal("history unexpectedly empty")
		}
		for i := 1; i < len(ids); i++ {
			if ids[i] != ids[i-1]-1 {
				t.Fatalf("non-contiguous history: %v", ids)
			}
		}
		if !communicationReplyFits(data) {
			t.Fatal("history exceeded response budget")
		}
		return ids
	}
	verifyOlder := func(t *testing.T, data map[string]interface{}) {
		t.Helper()
		ids := messageIDs(t, data)
		expected := ids[len(ids)-1] - 1
		for expected > 0 {
			cursor, _ := data["next_cursor"].(string)
			if data["has_more"] != true || cursor == "" {
				t.Fatalf("lost older continuation before %d", expected)
			}
			data = request(t, historyPath+url.Values{"cursor": {cursor}}.Encode())
			ids = messageIDs(t, data)
			if ids[0] != expected {
				t.Fatalf("continuation jumped from %d to %d", expected, ids[0])
			}
			expected = ids[len(ids)-1] - 1
		}
		if data["has_more"] != false || data["next_cursor"] != "" {
			t.Fatal("history reports continuation past oldest message")
		}
	}
	for _, test := range []struct{ anchor, limit, first, last int64 }{
		{100, 20, 100, 91}, {95, 20, 100, 86}, {50, 20, 60, 41}, {1, 20, 11, 1},
		{100, 1, 100, 100}, {50, 1, 50, 50}, {1, 1, 1, 1},
	} {
		t.Run(fmt.Sprintf("anchor_%d_limit_%d", test.anchor, test.limit), func(t *testing.T) {
			data := request(t, historyPath+url.Values{"anchor_message_id": {strconv.FormatInt(base+1000+test.anchor, 10)}, "limit": {strconv.FormatInt(test.limit, 10)}}.Encode())
			ids := messageIDs(t, data)
			if ids[0] != test.first || ids[len(ids)-1] != test.last {
				t.Fatalf("unexpected window: %v", ids)
			}
			verifyOlder(t, data)
		})
	}
	exec(`UPDATE private_messages SET content=? WHERE conv_id=?`, strings.Repeat("长", 56000), convID)
	for _, anchor := range []int64{100, 95, 50, 1} {
		t.Run(fmt.Sprintf("large_context_anchor_%d", anchor), func(t *testing.T) {
			data := request(t, historyPath+url.Values{"anchor_message_id": {strconv.FormatInt(base+1000+anchor, 10)}}.Encode())
			ids := messageIDs(t, data)
			found := false
			for _, id := range ids {
				found = found || id == anchor
			}
			if !found {
				t.Fatalf("budget trimming removed anchor %d: %v", anchor, ids)
			}
			verifyOlder(t, data)
		})
	}
	for _, test := range []struct{ content, query, match string }{
		{strings.Repeat("a", 1500) + "NEEDLE" + strings.Repeat("b", 1500), "needle", "NEEDLE"},
		{strings.Repeat("前", 1500) + "合同编号" + strings.Repeat("后", 1500), "合同编号", "合同编号"},
		{strings.Repeat("前", 1500) + "_%!" + strings.Repeat("后", 1500), "_%!", "_%!"},
	} {
		exec(`UPDATE private_messages SET content=? WHERE msg_id=?`, test.content, base+1100)
		data := request(t, "/search?"+url.Values{"q": {test.query}}.Encode())
		rows := data["results"].([]interface{})
		if len(rows) != 1 {
			t.Fatalf("search rows=%#v", rows)
		}
		row := rows[0].(map[string]interface{})
		preview := row["matched_message_preview"].(string)
		if !strings.Contains(preview, test.match) || utf8.RuneCountInString(preview) > 1000 || row["matched_message_id"] != strconv.FormatInt(base+1100, 10) {
			t.Fatalf("search preview lost match: %#v", row)
		}
	}
}
