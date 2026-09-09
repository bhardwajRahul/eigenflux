package consolev2

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"eigenflux_server/pkg/agentidentity"
)

func TestCommunicationConversationsRejectInvalidOriginFilter(t *testing.T) {
	svc := &Service{}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/api/v2/console/pm/conversations", svc.listCommunicationConversations)
	for _, test := range []struct {
		query string
		code  string
	}{
		{"origin_type=broadcast&origin_id=0", "INVALID_ORIGIN_ID"},
		{"origin_type=broadcast&origin_id=-1", "INVALID_ORIGIN_ID"},
		{"origin_type=broadcast&origin_id=bad", "INVALID_ORIGIN_ID"},
		{"origin_type=broadcast&origin_id=1.5", "INVALID_ORIGIN_ID"},
		{"origin_type=broadcast&origin_id=9223372036854775808", "INVALID_ORIGIN_ID"},
		{"origin_id=42", "INVALID_ORIGIN_TYPE"},
		{"origin_type=friend&origin_id=42", "INVALID_ORIGIN_TYPE"},
		{"origin_type=unbroken&origin_id=42", "INVALID_ORIGIN_TYPE"},
	} {
		t.Run(test.query, func(t *testing.T) {
			status, payload, _ := performJSON(t, h, http.MethodGet,
				"/api/v2/console/pm/conversations?"+test.query, nil)
			if status != http.StatusBadRequest || responseErrorCode(t, payload) != test.code {
				t.Fatalf("status=%d payload=%#v", status, payload)
			}
		})
	}
}

func TestPostgresCommunicationConversationsFilterBroadcast(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for broadcast conversation PostgreSQL contracts")
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

	now := time.Now().UnixMilli()
	base := time.Now().UnixNano()
	viewerID, originID := base, base+100
	for offset := int64(0); offset <= 6; offset++ {
		id := base + offset
		shortID, err := agentidentity.GenerateShortID()
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Exec(`INSERT INTO agents
			(agent_id, short_id, email, agent_name, bio, created_at, updated_at)
			VALUES (?, ?, ?, 'Discussion Agent', '', ?, ?)`, id, shortID,
			fmt.Sprintf("discussion-filter-%d@example.test", id), now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct {
		id, a, b, origin, updated int64
		originType                string
		status, count, topic      int
	}{
		{201, 0, 1, originID, now, "broadcast", 0, 1, 1},
		{202, 2, 0, originID, now, "broadcast", 0, 1, 1},
		{203, 0, 3, originID, now - 1, "broadcast", 0, 1, 0},
		{204, 0, 1, originID + 1, now + 1, "broadcast", 0, 1, 1},
		{205, 1, 2, originID, now + 2, "broadcast", 0, 1, 1},
		{206, 0, 4, originID, now + 3, "broadcast", 1, 1, 1},
		{207, 0, 5, originID, now + 4, "broadcast", 0, 0, 1},
		{208, 0, 6, originID, now + 5, "friend", 0, 1, 1},
	} {
		if err := tx.Exec(`INSERT INTO conversations
			(conv_id, participant_a, participant_b, initiator_id, last_sender_id,
			 origin_type, origin_id, msg_count, status, updated_at, topic_status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, base+row.id, base+row.a,
			base+row.b, base+row.a, base+row.b, row.originType, row.origin,
			row.count, row.status, row.updated, row.topic).Error; err != nil {
			t.Fatal(err)
		}
		if row.count > 0 {
			if err := tx.Exec(`INSERT INTO private_messages
				(msg_id, conv_id, sender_id, receiver_id, content, is_read, created_at)
				VALUES (?, ?, ?, ?, 'broadcast discussion', false, ?)`, base+row.id+1000,
				base+row.id, base+row.b, base+row.a, row.updated).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	svc := &Service{db: tx}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.GET("/api/v2/console/pm/conversations", func(ctx context.Context, c *app.RequestContext) {
		c.Set("agent_id", viewerID)
		svc.listCommunicationConversations(ctx, c)
	})
	requestPage := func(query url.Values) map[string]interface{} {
		t.Helper()
		status, payload, _ := performJSON(t, h, http.MethodGet,
			"/api/v2/console/pm/conversations?"+query.Encode(), nil)
		if status != http.StatusOK {
			t.Fatalf("status=%d payload=%#v", status, payload)
		}
		return responseData(t, payload)
	}
	conversationIDs := func(data map[string]interface{}) []int64 {
		t.Helper()
		var ids []int64
		if data["conversations"] == nil {
			return ids
		}
		for _, raw := range data["conversations"].([]interface{}) {
			row := raw.(map[string]interface{})
			ids = append(ids, mustParseInt64(t, row["conv_id"].(string))-base)
		}
		return ids
	}

	for _, test := range []struct {
		sort string
		want []int64
	}{
		{"recent", []int64{202, 201, 203}},
		{"topic_status", []int64{203, 201, 202}},
	} {
		t.Run(test.sort, func(t *testing.T) {
			query := url.Values{"origin_type": {"broadcast"}, "origin_id": {strconv.FormatInt(originID, 10)},
				"limit": {"2"}, "sort": {test.sort}}
			first := requestPage(query)
			if first["has_more"] != true || !reflect.DeepEqual(conversationIDs(first), test.want[:2]) {
				t.Fatalf("first page=%#v", first)
			}
			cursor, _ := first["next_cursor"].(string)
			if cursor == "" {
				t.Fatal("first page has no continuation cursor")
			}
			query.Set("cursor", cursor)
			second := requestPage(query)
			if second["has_more"] != false || second["next_cursor"] != "" || !reflect.DeepEqual(conversationIDs(second), test.want[2:]) {
				t.Fatalf("second page=%#v", second)
			}
		})
	}

	all := requestPage(url.Values{"origin_type": {"broadcast"}})
	if !reflect.DeepEqual(conversationIDs(all), []int64{204, 202, 201, 203}) {
		t.Fatalf("unfiltered conversations changed: %#v", all)
	}
	viewerID = base + 99
	foreign := requestPage(url.Values{"origin_type": {"broadcast"}, "origin_id": {strconv.FormatInt(originID, 10)}})
	if len(conversationIDs(foreign)) != 0 || len(foreign["agent_contexts"].(map[string]interface{})) != 0 || foreign["has_more"] != false {
		t.Fatalf("non-participant can see broadcast discussions: %#v", foreign)
	}
}
