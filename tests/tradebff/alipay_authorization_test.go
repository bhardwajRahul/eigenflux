package tradebff_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eigenflux_server/api/tradebff"
	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/network/standard"
	hertztracing "github.com/hertz-contrib/obs-opentelemetry/tracing"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAlipayAuthorizationVerificationOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, payload, query, want string
		status                     int
		transportFailure           bool
	}{
		{name: "disabled", status: 501, payload: `{"code":501,"error_code":"PAYMENT_UNSUPPORTED","msg":"private-diagnostic"}`, want: "failed"},
		{name: "upstream unavailable", status: 503, payload: `{"code":503}`, want: "failed"},
		{name: "delegation denied", status: 403, payload: `{"code":403,"error_code":"AUTH_FORBIDDEN"}`, want: "failed"},
		{name: "rate limited", status: 429, payload: `{"code":429}`, want: "failed"},
		{name: "unclassified bad request", status: 400, payload: `{"code":400}`, want: "failed"},
		{name: "transport", transportFailure: true, want: "failed"},
		{name: "invalid JSON", status: 200, payload: `not-json`, want: "failed"},
		{name: "missing account", status: 200, payload: `{"code":0,"data":{}}`, want: "failed"},
		{name: "verification denied", status: 400, payload: `{"code":400,"error_code":"PAYMENT_INVALID_ARGUMENT","msg":"private-diagnostic"}`, want: "rejected"},
		{name: "provider denied", query: "&error=access_denied", want: "rejected"},
		{name: "authorized", status: 200, payload: `{"code":0,"data":{"masked_display":"****9012"}}`, want: "authorized"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var verifies atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/api/v1/wallet/alipay/authorization/verify", r.URL.Path)
				verifies.Add(1)
				if tc.transportFailure {
					conn, _, err := w.(http.Hijacker).Hijack()
					require.NoError(t, err)
					conn.Close()
					return
				}
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.payload)
			}))
			defer upstream.Close()
			_, private, err := ed25519.GenerateKey(rand.Reader)
			require.NoError(t, err)
			trade, err := tradebff.New(tradebff.Config{Endpoint: upstream.URL, DelegationKeyID: "test", DelegationPrivateKey: base64.RawURLEncoding.EncodeToString(private)})
			require.NoError(t, err)
			r := miniredis.RunT(t)
			rdb := redis.NewClient(&redis.Options{Addr: r.Addr()})
			defer rdb.Close()
			auth := tradebff.NewAlipayAuthorization(trade, rdb, tradebff.AlipayAuthorizationConfig{AppID: "2026090800000001", CallbackURL: "https://console.example" + tradebff.AlipayAuthorizationCallbackPath, Production: true})
			h := server.New()
			identity := func(ctx context.Context, c *app.RequestContext) {
				c.Set("agent_id", int64(42))
				c.Set("console_session_id", "owner")
				c.Next(ctx)
			}
			h.POST("/start", identity, auth.Start)
			h.GET("/status/:authorization_id", identity, auth.Status)
			h.POST("/confirm/:authorization_id", identity, auth.Confirm)
			h.GET("/callback", auth.Callback)
			startBody := `{"expected_agent_id":"42"}`
			start := ut.PerformRequest(h.Engine, "POST", "/start", &ut.Body{Body: strings.NewReader(startBody), Len: len(startBody)}, ut.Header{Key: "Idempotency-Key", Value: "start"}).Result()
			require.Equal(t, 200, start.StatusCode())
			var response struct {
				Data struct {
					ID     string `json:"authorization_id"`
					URL    string `json:"authorization_url"`
					Status string `json:"status"`
				}
			}
			require.NoError(t, json.Unmarshal(start.Body(), &response))
			u, err := url.Parse(response.Data.URL)
			require.NoError(t, err)
			state := u.Query().Get("state")
			callback := "/callback?state=" + state + "&auth_code=private-code" + tc.query
			for i := 0; i < 2; i++ {
				result := ut.PerformRequest(h.Engine, "GET", callback, nil).Result()
				require.Equal(t, 303, result.StatusCode())
				require.Equal(t, tradebff.AlipayAuthorizationResultPath, string(result.Header.Peek("Location")))
			}
			wantCalls := int32(1)
			if tc.query != "" {
				wantCalls = 0
			}
			require.Equal(t, wantCalls, verifies.Load(), "callback state must be consumed once")
			result := ut.PerformRequest(h.Engine, "GET", "/status/"+response.Data.ID, nil).Result()
			require.Equal(t, 200, result.StatusCode())
			require.NoError(t, json.Unmarshal(result.Body(), &response))
			require.Equal(t, tc.want, response.Data.Status)
			for _, secret := range []string{"private-code", "private-diagnostic", state, "PAYMENT_UNSUPPORTED"} {
				require.NotContains(t, string(result.Body()), secret)
			}
			if tc.want != "authorized" {
				confirm := ut.PerformRequest(h.Engine, "POST", "/confirm/"+response.Data.ID, nil, ut.Header{Key: "Idempotency-Key", Value: "confirm"}).Result()
				require.Equal(t, 409, confirm.StatusCode())
				for _, key := range r.Keys() {
					value, _ := r.Get(key)
					require.NotContains(t, value, "private-code")
					require.NotContains(t, value, "private-diagnostic")
				}
			}
		})
	}
}

func TestAlipayAuthorizationCrossDeviceAndExplicitConfirmation(t *testing.T) {
	var verifies, binds int
	var bindKeys []string
	malformedBind := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
		require.NotEmpty(t, r.Header.Get("Idempotency-Key"))
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "private-code", body["authorization"])
		switch r.URL.Path {
		case "/api/v1/wallet/alipay/authorization/verify":
			verifies++
			io.WriteString(w, `{"code":0,"data":{"masked_display":"支付宝用户号 ****9012"}}`)
		case "/api/v1/wallet/binding":
			binds++
			bindKeys = append(bindKeys, r.Header.Get("Idempotency-Key"))
			if malformedBind {
				io.WriteString(w, `{"code":0,"data":{}}`)
			} else {
				io.WriteString(w, `{"code":0,"data":{"binding":{"binding_id":"9007199254740993"}}}`)
			}
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	trade, err := tradebff.New(tradebff.Config{Endpoint: upstream.URL, DelegationKeyID: "test", DelegationPrivateKey: base64.RawURLEncoding.EncodeToString(private)})
	require.NoError(t, err)
	r := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer rdb.Close()
	auth := tradebff.NewAlipayAuthorization(trade, rdb, tradebff.AlipayAuthorizationConfig{AppID: "2026090800000001", CallbackURL: "https://console.example" + tradebff.AlipayAuthorizationCallbackPath, Production: true})
	require.NotNil(t, auth)
	h := server.New()
	identity := func(ctx context.Context, c *app.RequestContext) {
		if c.GetHeader("X-Test-Session") != nil {
			c.Set("agent_id", int64(42))
			c.Set("console_session_id", string(c.GetHeader("X-Test-Session")))
		}
		c.Next(ctx)
	}
	h.POST("/start", identity, auth.Start)
	h.GET("/status/:authorization_id", identity, auth.Status)
	h.POST("/confirm/:authorization_id", identity, auth.Confirm)
	h.GET("/callback", auth.Callback)
	call := func(method, path, session, body, key string) (int, []byte) {
		headers := []ut.Header{{Key: "Content-Type", Value: "application/json"}, {Key: "Idempotency-Key", Value: key}}
		if session != "" {
			headers = append(headers, ut.Header{Key: "X-Test-Session", Value: session})
		}
		var requestBody *ut.Body
		if body != "" {
			requestBody = &ut.Body{Body: strings.NewReader(body), Len: len(body)}
		}
		res := ut.PerformRequest(h.Engine, method, path, requestBody, headers...).Result()
		return res.StatusCode(), res.Body()
	}
	status, _ := call("POST", "/start", "", `{"expected_agent_id":"42"}`, "one")
	require.Equal(t, 401, status)
	status, _ = call("POST", "/start", "owner", `{"expected_agent_id":"99"}`, "one")
	require.Equal(t, 409, status)
	status, body := call("POST", "/start", "owner", `{"expected_agent_id":"42"}`, "one")
	require.Equal(t, 200, status)
	var response struct {
		Data struct {
			ID     string `json:"authorization_id"`
			URL    string `json:"authorization_url"`
			Status string `json:"status"`
		}
	}
	require.NoError(t, json.Unmarshal(body, &response))
	id := response.Data.ID
	require.Len(t, id, 64)
	u, err := url.Parse(response.Data.URL)
	require.NoError(t, err)
	state := u.Query().Get("state")
	require.NotEqual(t, id, state)
	require.Equal(t, "auth_user", u.Query().Get("scope"))
	_, again := call("POST", "/start", "owner", `{"expected_agent_id":"42"}`, "one")
	require.JSONEq(t, string(body), string(again))
	status, _ = call("GET", "/status/"+id, "different", "", "")
	require.Equal(t, 403, status)
	status, _ = call("POST", "/confirm/"+id, "owner", "{}", "confirm")
	require.Equal(t, 409, status)
	require.Zero(t, binds)
	status, body = call("GET", "/callback?state="+state+"&auth_code=private-code", "", "", "")
	require.Equal(t, 303, status)
	require.NotContains(t, string(body), "private-code")
	require.Equal(t, 1, verifies)
	require.Zero(t, binds)
	call("GET", "/callback?state="+state+"&auth_code=private-code", "", "", "")
	require.Equal(t, 1, verifies)
	status, body = call("GET", "/status/"+id, "owner", "", "")
	require.Equal(t, 200, status)
	require.Contains(t, string(body), "authorized")
	require.NotContains(t, string(body), "private-code")
	require.NotContains(t, string(body), state)
	status, _ = call("POST", "/confirm/"+id, "different", "{}", "confirm")
	require.Equal(t, 403, status)
	require.Zero(t, binds)
	malformedBind = true
	status, _ = call("POST", "/confirm/"+id, "owner", "{}", "confirm")
	require.Equal(t, 502, status)
	malformedBind = false
	status, body = call("POST", "/confirm/"+id, "owner", "{}", "different-key")
	require.Equal(t, 200, status)
	require.Contains(t, string(body), "confirmed")
	require.Equal(t, bindKeys[0], bindKeys[1])
	status, _ = call("POST", "/confirm/"+id, "owner", "{}", "again")
	require.Equal(t, 200, status)
	require.Equal(t, 2, binds)
	for _, key := range r.Keys() {
		value, _ := r.Get(key)
		require.NotContains(t, value, "private-code", "confirmed attempt must discard the code")
	}
	r.FastForward(11 * time.Minute)
	status, _ = call("GET", "/status/"+id, "owner", "", "")
	require.Equal(t, 410, status)
}

func TestAlipayTraceRealHTTPPreservesRequestLifecycle(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	tracer, cfg := hertztracing.NewServerTracer(hertztracing.WithShouldIgnore(tradebff.IgnoreAlipayAuthorizationTrace))
	h := server.New(server.WithListener(ln), server.WithTransport(standard.NewTransporter), tracer)
	h.Use(hertztracing.ServerMiddleware(cfg))
	for _, route := range []string{"/ordinary", tradebff.AlipayAuthorizationCallbackPath} {
		h.GET(route, func(_ context.Context, c *app.RequestContext) { c.String(200, "ok") })
	}
	done := make(chan error, 1)
	go func() { done <- h.Run() }()
	defer func() { h.Engine.Close(); <-done }()
	require.Eventually(t, h.IsRunning, 3*time.Second, 10*time.Millisecond)
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	for _, route := range []string{"/ordinary", tradebff.AlipayAuthorizationCallbackPath + "?auth_code=fixture", "/ordinary"} {
		res, err := client.Get("http://" + ln.Addr().String() + route)
		require.NoError(t, err)
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		require.Equal(t, "ok", string(body))
	}
}

func TestAlipayAuthorizationUnavailableConfiguration(t *testing.T) {
	var auth *tradebff.AlipayAuthorization
	c := app.NewContext(0)
	c.Set("agent_id", int64(42))
	c.Set("console_session_id", "owner")
	auth.Start(context.Background(), c)
	require.Equal(t, 503, c.Response.StatusCode())
	require.Nil(t, tradebff.NewAlipayAuthorization(nil, nil, tradebff.AlipayAuthorizationConfig{}))
	c.Request.SetRequestURI(tradebff.AlipayAuthorizationCallbackPath + "?auth_code=private-code&state=private-state")
	require.False(t, tradebff.IgnoreAlipayAuthorizationTrace(context.Background(), c))
	require.False(t, c.Request.IsURIParsed(), "tracer Start must not parse request URI")
	c.Request.URI() // Hertz parses URI before invoking middleware.
	require.True(t, tradebff.IgnoreAlipayAuthorizationTrace(context.Background(), c))
	c.Request.SetRequestURI("/api/v2/console/bff/trade/overview")
	require.False(t, tradebff.IgnoreAlipayAuthorizationTrace(context.Background(), c))
	require.False(t, c.Request.IsURIParsed())
	c.Request.URI()
	require.False(t, tradebff.IgnoreAlipayAuthorizationTrace(context.Background(), c))
}
