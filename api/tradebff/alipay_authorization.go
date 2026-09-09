package tradebff

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/redis/go-redis/v9"
)

const AlipayAuthorizationCallbackPath = "/api/v2/console/alipay/authorization/callback"
const AlipayAuthorizationResultPath = "/api/v2/console/alipay/authorization/result"
const payoutAuthorizationTTL = 10 * time.Minute
const payoutAuthorizationPrefix = "console:alipay:authorization:"

// The stock HTTP tracer records the full URL. This callback carries a one-use
// credential and must not enter that tracer (proxy logs need equivalent redaction).
func IgnoreAlipayAuthorizationTrace(_ context.Context, c *app.RequestContext) bool {
	return string(c.Path()) == AlipayAuthorizationCallbackPath
}

type AlipayAuthorizationConfig struct {
	AppID, CallbackURL string
	Production         bool
}
type AlipayAuthorization struct {
	trade  *Service
	redis  redis.UniversalClient
	config AlipayAuthorizationConfig
}
type authorizationAttempt struct {
	ID            string `json:"authorization_id"`
	AgentID       int64  `json:"agent_id"`
	Owner         string `json:"owner"`
	State         string `json:"state"`
	Status        string `json:"status"`
	Code          string `json:"code,omitempty"`
	MaskedDisplay string `json:"masked_display,omitempty"`
	ExpiresAt     int64  `json:"expires_at"`
}

func NewAlipayAuthorization(trade *Service, rdb redis.UniversalClient, cfg AlipayAuthorizationConfig) *AlipayAuthorization {
	u, err := url.Parse(cfg.CallbackURL)
	if trade == nil || trade.client == nil || trade.delegator == nil || rdb == nil || len(cfg.AppID) < 8 || len(cfg.AppID) > 32 || err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != AlipayAuthorizationCallbackPath || u.RawQuery != "" || u.Fragment != "" {
		return nil
	}
	for _, c := range cfg.AppID {
		if c < '0' || c > '9' {
			return nil
		}
	}
	return &AlipayAuthorization{trade: trade, redis: rdb, config: cfg}
}

func authorizationHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func authorizationRandom() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
func validAuthorizationID(id string) bool {
	if len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func authorizationOwner(c *app.RequestContext) (int64, string, bool) {
	id, ok := agentID(c)
	value, exists := c.Get("console_session_id")
	session, valid := value.(string)
	if !ok || !exists || !valid || session == "" {
		return 0, "", false
	}
	return id, authorizationHash(strconv.FormatInt(id, 10) + ":" + session), true
}
func authorizationError(c *app.RequestContext, status int) {
	replyError(c, status, "ALIPAY_AUTHORIZATION_UNAVAILABLE", http.StatusText(status))
}
func (s *AlipayAuthorization) available(c *app.RequestContext) bool {
	if s == nil {
		authorizationError(c, 503)
		return false
	}
	return true
}
func (s *AlipayAuthorization) projection(a *authorizationAttempt) map[string]interface{} {
	out := map[string]interface{}{"authorization_id": a.ID, "status": a.Status, "expires_at": time.UnixMilli(a.ExpiresAt).UTC().Format(time.RFC3339)}
	if a.MaskedDisplay != "" {
		out["masked_display"] = a.MaskedDisplay
	}
	if a.Status == "pending" && a.ExpiresAt > time.Now().UnixMilli() {
		host := "openauth.alipay.com"
		if !s.config.Production {
			host = "openauth.alipaydev.com"
		}
		u := url.URL{Scheme: "https", Host: host, Path: "/oauth2/publicAppAuthorize.htm"}
		u.RawQuery = url.Values{"app_id": {s.config.AppID}, "scope": {"auth_user"}, "redirect_uri": {s.config.CallbackURL}, "state": {a.State}}.Encode()
		out["authorization_url"] = u.String()
	}
	return out
}
func (s *AlipayAuthorization) load(ctx context.Context, id string) (*authorizationAttempt, error) {
	if !validAuthorizationID(id) {
		return nil, redis.Nil
	}
	data, err := s.redis.Get(ctx, payoutAuthorizationPrefix+id).Bytes()
	if err != nil {
		return nil, err
	}
	var a authorizationAttempt
	if json.Unmarshal(data, &a) != nil || a.ID != id {
		return nil, errors.New("invalid authorization state")
	}
	return &a, nil
}
func (s *AlipayAuthorization) owned(ctx context.Context, c *app.RequestContext) (*authorizationAttempt, bool) {
	id, owner, ok := authorizationOwner(c)
	if !ok {
		authorizationError(c, 401)
		return nil, false
	}
	if !s.available(c) {
		return nil, false
	}
	a, err := s.load(ctx, c.Param("authorization_id"))
	if errors.Is(err, redis.Nil) {
		authorizationError(c, 410)
		return nil, false
	}
	if err != nil {
		authorizationError(c, 503)
		return nil, false
	}
	if a.AgentID != id || a.Owner != owner {
		authorizationError(c, 403)
		return nil, false
	}
	if a.ExpiresAt <= time.Now().UnixMilli() && a.Status != "confirmed" {
		a.Status = "expired"
	}
	return a, true
}

func (s *AlipayAuthorization) Start(ctx context.Context, c *app.RequestContext) {
	id, owner, ok := authorizationOwner(c)
	if !ok {
		authorizationError(c, 401)
		return
	}
	if !s.available(c) {
		return
	}
	var input struct {
		ExpectedAgentID string `json:"expected_agent_id"`
	}
	if len(c.Request.Body()) > 1024 || json.Unmarshal(c.Request.Body(), &input) != nil || input.ExpectedAgentID != strconv.FormatInt(id, 10) {
		authorizationError(c, http.StatusConflict)
		return
	}
	key := strings.TrimSpace(string(c.GetHeader("Idempotency-Key")))
	if key == "" || len(key) > 64 {
		authorizationError(c, 400)
		return
	}
	attemptID, err := authorizationRandom()
	if err != nil {
		authorizationError(c, 503)
		return
	}
	state, err := authorizationRandom()
	if err != nil {
		authorizationError(c, 503)
		return
	}
	a := authorizationAttempt{ID: attemptID, AgentID: id, Owner: owner, State: state, Status: "pending", ExpiresAt: time.Now().Add(payoutAuthorizationTTL).UnixMilli()}
	data, _ := json.Marshal(a)
	// Atomic idempotent initiation; neither a failed response nor concurrent
	// clicks create multiple attempts for the same key.
	result, err := s.redis.Eval(ctx, `local old=redis.call('GET',KEYS[1]);if old then return old end;redis.call('SET',KEYS[1],ARGV[1],'PX',ARGV[3]);redis.call('SET',KEYS[2],ARGV[2],'PX',ARGV[3]);redis.call('SET',KEYS[3],ARGV[1],'PX',ARGV[3]);return ARGV[1]`, []string{payoutAuthorizationPrefix + "start:" + authorizationHash(owner+":"+key), payoutAuthorizationPrefix + attemptID, payoutAuthorizationPrefix + "state:" + authorizationHash(state)}, attemptID, string(data), payoutAuthorizationTTL.Milliseconds()).Text()
	if err != nil {
		authorizationError(c, 503)
		return
	}
	loaded, err := s.load(ctx, result)
	if err != nil {
		authorizationError(c, 503)
		return
	}
	reply(c, 200, s.projection(loaded))
}
func (s *AlipayAuthorization) Status(ctx context.Context, c *app.RequestContext) {
	a, ok := s.owned(ctx, c)
	if ok {
		reply(c, 200, s.projection(a))
	}
}

func (s *AlipayAuthorization) Callback(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	// Always leave the credential-bearing URL. The result page contains no
	// account details and asks the user to check the initiating Console.
	defer c.Redirect(http.StatusSeeOther, []byte(AlipayAuthorizationResultPath))
	if s == nil {
		return
	}
	state := c.Query("state")
	if !validAuthorizationID(state) {
		return
	}
	id, err := s.redis.GetDel(ctx, payoutAuthorizationPrefix+"state:"+authorizationHash(state)).Result()
	if err != nil {
		return
	}
	a, err := s.load(ctx, id)
	if err != nil || a.State != state || a.Status != "pending" || a.ExpiresAt <= time.Now().UnixMilli() {
		return
	}
	a.Status = "rejected"
	code := c.Query("auth_code")
	if code != "" && len(code) <= 512 && c.Query("error") == "" {
		body, _ := json.Marshal(map[string]string{"authorization": code})
		result, err := s.trade.fetch(ctx, a.AgentID, "payout:bind", "wallet.authorization.verify", http.MethodPost, "/api/v1/wallet/alipay/authorization/verify", nil, body, "verify-"+id[:48], true)
		if err == nil {
			var preview struct {
				MaskedDisplay string `json:"masked_display"`
			}
			if json.Unmarshal(result, &preview) == nil && preview.MaskedDisplay != "" {
				a.Status = "authorized"
				a.Code = code
				a.MaskedDisplay = preview.MaskedDisplay
			}
		}
	}
	data, _ := json.Marshal(a)
	remaining := time.Until(time.UnixMilli(a.ExpiresAt))
	if remaining > 0 {
		_ = s.redis.Set(ctx, payoutAuthorizationPrefix+id, data, remaining).Err()
	}
}

func AlipayAuthorizationResult(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "private, no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, "text/html; charset=utf-8", []byte(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>支付宝授权</title><h1>请返回原控制台查看授权结果</h1><p>若授权成功，请核对账户并点击“确认用于提现”。本页不会自动绑定账户，也不会发起付款或提现。</p></html>`))
}

func (s *AlipayAuthorization) Confirm(ctx context.Context, c *app.RequestContext) {
	a, ok := s.owned(ctx, c)
	if !ok {
		return
	}
	if a.Status == "confirmed" {
		reply(c, 200, s.projection(a))
		return
	}
	if a.Status != "authorized" || a.Code == "" {
		authorizationError(c, 409)
		return
	}
	key := strings.TrimSpace(string(c.GetHeader("Idempotency-Key")))
	if key == "" || len(key) > 64 {
		authorizationError(c, 400)
		return
	}
	body, _ := json.Marshal(map[string]string{"authorization": a.Code})
	// The server chooses the stable binding key, never the browser. Concurrent
	// confirmations and ambiguous retries converge in Wallet's transaction.
	result, err := s.trade.fetch(ctx, a.AgentID, "payout:bind", "wallet.binding.bind", http.MethodPost, "/api/v1/wallet/binding", nil, body, "bind-"+a.ID[:48], true)
	if err != nil {
		authorizationError(c, upstreamStatus(err))
		return
	}
	var bound struct {
		Binding *struct {
			ID json.Number `json:"binding_id"`
		} `json:"binding"`
	}
	if json.Unmarshal(result, &bound) != nil || bound.Binding == nil || !positiveDecimal(bound.Binding.ID.String()) {
		authorizationError(c, http.StatusBadGateway)
		return
	}
	a.Status = "confirmed"
	a.Code = ""
	a.State = ""
	data, _ := json.Marshal(a)
	if err := s.redis.Set(ctx, payoutAuthorizationPrefix+a.ID, data, payoutAuthorizationTTL).Err(); err != nil {
		authorizationError(c, 503)
		return
	}
	reply(c, 200, s.projection(a))
}
