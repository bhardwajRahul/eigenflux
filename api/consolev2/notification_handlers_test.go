package consolev2

import (
	"context"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/kitex/client/callopt"

	"eigenflux_server/kitex_gen/eigenflux/base"
	notificationrpc "eigenflux_server/kitex_gen/eigenflux/notification"
)

type capturingNotificationClient struct {
	listAgentID int64
	ackAgentID  int64
	payload     string
	failAck     bool
}

func (c *capturingNotificationClient) ListPending(_ context.Context, req *notificationrpc.ListPendingReq, _ ...callopt.Option) (*notificationrpc.ListPendingResp, error) {
	c.listAgentID = req.AgentId
	return &notificationrpc.ListPendingResp{
		BaseResp: &base.BaseResp{},
		Notifications: []*notificationrpc.PendingNotification{{
			NotificationId: 9007199254740993, SourceType: "commission_order",
			Type: "order.state.changed.v1", PayloadJson: &c.payload,
		}},
	}, nil
}

func (c *capturingNotificationClient) AckNotifications(_ context.Context, req *notificationrpc.AckNotificationsReq, _ ...callopt.Option) (*notificationrpc.AckNotificationsResp, error) {
	c.ackAgentID = req.AgentId
	code := int32(0)
	if c.failAck {
		code = 500
	}
	return &notificationrpc.AckNotificationsResp{BaseResp: &base.BaseResp{Code: code}}, nil
}

func TestConsoleNotificationRoutesUseAuthenticatedAgentAndStringIDs(t *testing.T) {
	fixture := newBrowserSessionFixture(t)
	fixture.add(t, 0, 42, false)
	fixture.add(t, 1, 43, false)
	if err := fixture.service.db.Exec(`CREATE TABLE agent_onboarding_v2 (agent_id INTEGER PRIMARY KEY, state TEXT, current_step INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.db.Exec(`INSERT INTO agent_onboarding_v2 VALUES (42, 'completed', 1), (43, 'completed', 1)`).Error; err != nil {
		t.Fatal(err)
	}
	client := &capturingNotificationClient{payload: `{"event_id":9007199254740993,"order_id":9007199254740995,"recipient_agent_id":42,"actor_agent_id":0,"snapshot_id":9007199254740997,"order_version":2}`}
	fixture.service.notificationClient = client
	h := server.New()
	h.GET("/api/v2/console/notifications/pending", fixture.service.consoleAuth(false), fixture.service.requireCompleted, fixture.service.listPendingNotifications)
	h.POST("/api/v2/console/notifications/ack", fixture.service.consoleAuth(true), fixture.service.requireCompleted, fixture.service.ackPendingNotifications)

	cookie := fixture.cookieHeader(0)
	status, response, _ := performJSON(t, h, http.MethodGet, "/api/v2/console/notifications/pending?agent_id=43", struct{}{}, ut.Header{Key: "Cookie", Value: cookie})
	if status != http.StatusOK || client.listAgentID != 42 {
		t.Fatalf("pending status=%d RPC agent=%d response=%#v", status, client.listAgentID, response)
	}
	notification := responseData(t, response)["notifications"].([]interface{})[0].(map[string]interface{})
	payload := notification["payload"].(map[string]interface{})
	if notification["notification_id"] != "9007199254740993" || payload["order_id"] != "9007199254740995" {
		t.Fatalf("notification IDs are not browser-safe: %#v", notification)
	}

	status, response, _ = performJSON(t, h, http.MethodPost, "/api/v2/console/notifications/ack", map[string]interface{}{
		"notifications": []map[string]interface{}{{"notification_id": "9007199254740993", "source_type": "commission_order"}},
	}, ut.Header{Key: "Cookie", Value: cookie}, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
	if status != http.StatusOK || client.ackAgentID != 42 {
		t.Fatalf("ack status=%d RPC agent=%d response=%#v", status, client.ackAgentID, response)
	}

	unauthorized := ut.PerformRequest(h.Engine, http.MethodGet, "/api/v2/console/notifications/pending", nil).Result()
	if unauthorized.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("unauthenticated pending status=%d body=%s", unauthorized.StatusCode(), unauthorized.Body())
	}
}

func TestConsoleNotificationAckPropagatesPersistenceFailure(t *testing.T) {
	fixture := newBrowserSessionFixture(t)
	fixture.add(t, 0, 42, false)
	if err := fixture.service.db.Exec(`CREATE TABLE agent_onboarding_v2 (agent_id INTEGER PRIMARY KEY, state TEXT, current_step INTEGER)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.db.Exec(`INSERT INTO agent_onboarding_v2 VALUES (42, 'completed', 1)`).Error; err != nil {
		t.Fatal(err)
	}
	fixture.service.notificationClient = &capturingNotificationClient{failAck: true}
	h := server.New()
	h.POST("/api/v2/console/notifications/ack", fixture.service.consoleAuth(true), fixture.service.requireCompleted, fixture.service.ackPendingNotifications)
	status, _, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/notifications/ack", map[string]interface{}{
		"notifications": []map[string]interface{}{{"notification_id": "81", "source_type": "commission_order"}},
	}, ut.Header{Key: "Cookie", Value: fixture.cookieHeader(0)}, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
	if status != http.StatusServiceUnavailable {
		t.Fatalf("failed ACK status=%d, want %d", status, http.StatusServiceUnavailable)
	}
}
