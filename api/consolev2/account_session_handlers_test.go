package consolev2

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type browserSessionFixture struct {
	service *Service
	now     int64
	cookies []string
}

func newBrowserSessionFixture(t *testing.T) *browserSessionFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	for _, statement := range []string{
		`CREATE TABLE agents (agent_id INTEGER PRIMARY KEY, identity_state TEXT, agent_name TEXT, agent_name_en TEXT, short_id TEXT)`,
		`CREATE TABLE agent_principals (principal_id INTEGER PRIMARY KEY, agent_id INTEGER, status TEXT, revoked_at INTEGER)`,
		`CREATE TABLE agent_email_bindings (binding_id INTEGER PRIMARY KEY, agent_id INTEGER, normalized_email TEXT, status TEXT, verification_state TEXT, updated_at INTEGER)`,
		`CREATE TABLE console_v2_sessions (
			session_id TEXT PRIMARY KEY, agent_id INTEGER, principal_id INTEGER,
			session_secret_hash TEXT, csrf_secret_hash TEXT, scopes TEXT,
			idle_expires_at INTEGER, absolute_expires_at INTEGER, last_seen_at INTEGER,
			auth_method TEXT, recent_auth_at INTEGER, status TEXT, revoked_at INTEGER
		)`,
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &browserSessionFixture{service: &Service{db: db, publicURL: "https://console.example.test"}, now: time.Now().UnixMilli()}
}

func (f *browserSessionFixture) add(t *testing.T, slot int, agentID int64, expired bool) string {
	t.Helper()
	sessionID := fmt.Sprintf("session-%d-%d", agentID, slot)
	expiresAt := f.now + int64(time.Hour/time.Millisecond)
	if expired {
		expiresAt = f.now - 1
	}
	for _, statement := range []string{
		`INSERT OR IGNORE INTO agents (agent_id, identity_state, agent_name, agent_name_en, short_id) VALUES (?, 'active', 'Same display name', '', 'AbCdE')`,
		`INSERT OR IGNORE INTO agent_principals (principal_id, agent_id, status) VALUES (?, ?, 'active')`,
	} {
		args := []interface{}{agentID}
		if strings.Contains(statement, "agent_principals") {
			args = append(args, agentID)
		}
		if err := f.service.db.Exec(statement, args...).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := f.service.db.Exec(`INSERT INTO console_v2_sessions
		(session_id, agent_id, principal_id, session_secret_hash, csrf_secret_hash, scopes,
		idle_expires_at, absolute_expires_at, last_seen_at, auth_method, status)
		VALUES (?, ?, ?, ?, ?, '{}', ?, ?, ?, 'email_otp', 'active')`, sessionID, agentID, agentID,
		hashString("secret"), hashString("csrf"), expiresAt, expiresAt, f.now-int64(slot)).Error; err != nil {
		t.Fatal(err)
	}
	if slot >= 0 {
		f.cookies = append(f.cookies, consoleSessionCookieName(slot)+"="+sessionID+".secret")
	}
	return sessionID
}

func (f *browserSessionFixture) context(activeSlot int) *app.RequestContext {
	c := app.NewContext(0)
	c.Request.Header.Set("Cookie", f.cookieHeader(activeSlot))
	return c
}

func (f *browserSessionFixture) cookieHeader(activeSlot int) string {
	return strings.Join(append(append([]string{}, f.cookies...), fmt.Sprintf("%s=%d", activeConsoleSlotCookieName, activeSlot)), "; ")
}

func TestConsoleAccountsDeduplicateBrowserSessions(t *testing.T) {
	f := newBrowserSessionFixture(t)
	f.add(t, 0, 10, true)
	f.add(t, 1, 20, false)
	f.add(t, 2, 10, false)
	f.add(t, 3, 10, false)
	for _, test := range []struct{ activeSlot, expectedSlot int }{{0, 2}, {3, 3}, {1, 2}} {
		accounts := f.service.consoleAccounts(f.service.db, f.context(test.activeSlot), f.now)
		if len(accounts) != 2 {
			t.Fatalf("active slot %d returned %d accounts: %#v", test.activeSlot, len(accounts), accounts)
		}
		for _, account := range accounts {
			if account.AgentID == "10" && (account.Expired || account.Slot != test.expectedSlot) {
				t.Fatalf("wrong representative for duplicate account: %#v", account)
			}
		}
	}
}

func TestEmailLoginReusesExistingBrowserAccountSlot(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprintf("expired=%t", expired), func(t *testing.T) {
			f := newBrowserSessionFixture(t)
			first := f.add(t, 0, 10, false)
			target := f.add(t, 2, 20, expired)
			c := f.context(0)
			slot, sessionID := f.service.chooseEmailLoginSessionSlot(f.service.db, c, 20)
			if slot != 2 || sessionID != target {
				t.Fatalf("existing target got slot=%d session=%q", slot, sessionID)
			}
			slot, sessionID = f.service.chooseEmailLoginSessionSlot(f.service.db, c, 30)
			if slot != 0 || sessionID != first {
				t.Fatalf("new normal login changed slot-zero replacement: slot=%d session=%q", slot, sessionID)
			}
		})
	}
}

func TestConsoleSlotSelectionPrefersTargetAndReclaimsDuplicates(t *testing.T) {
	f := newBrowserSessionFixture(t)
	for slot, agentID := range []int64{10, 20, 30, 20, 40} {
		f.add(t, slot, agentID, false)
	}
	c := f.context(3)
	slot, sessionID, accounts, err := f.service.chooseConsoleSessionSlot(f.service.db, c, 20, 10, f.now)
	if err != nil || slot != 3 || sessionID != "session-20-3" || len(accounts) != 4 {
		t.Fatalf("existing target must win over replacement: slot=%d session=%q accounts=%#v err=%v", slot, sessionID, accounts, err)
	}
	slot, sessionID, _, err = f.service.chooseConsoleSessionSlot(f.service.db, c, 50, 0, f.now)
	if err != nil || slot != 3 || sessionID != "session-20-3" {
		t.Fatalf("duplicate slot was counted as another account: slot=%d session=%q err=%v", slot, sessionID, err)
	}
}

func TestConsoleSlotSelectionPreservesFiveDistinctAccountLimit(t *testing.T) {
	f := newBrowserSessionFixture(t)
	for slot := 0; slot < maxConsoleAccountSlots; slot++ {
		f.add(t, slot, int64(slot+10), false)
	}
	_, _, accounts, err := f.service.chooseConsoleSessionSlot(f.service.db, f.context(0), 99, 0, f.now)
	if err != errConsoleAccountLimit || len(accounts) != maxConsoleAccountSlots {
		t.Fatalf("distinct-account limit changed: accounts=%#v err=%v", accounts, err)
	}
}

func TestConsoleSlotSelectionReclaimsCookieAliasWithoutRevokingSharedSession(t *testing.T) {
	f := newBrowserSessionFixture(t)
	f.add(t, 0, 10, false)
	shared := f.add(t, 1, 20, false)
	f.add(t, 2, 30, false)
	f.cookies = append(f.cookies, consoleSessionCookieName(3)+"="+shared+".secret")
	f.add(t, 4, 40, false)
	slot, revokedSessionID, _, err := f.service.chooseConsoleSessionSlot(f.service.db, f.context(0), 50, 0, f.now)
	if err != nil || slot != 3 || revokedSessionID != "" {
		t.Fatalf("reclaiming an alias would revoke the retained account's only session: slot=%d revoked=%q err=%v", slot, revokedSessionID, err)
	}
}

func TestRemoveAndLogoutClearEveryBrowserSlotForAccount(t *testing.T) {
	for _, logout := range []bool{false, true} {
		t.Run(fmt.Sprintf("logout=%t", logout), func(t *testing.T) {
			f := newBrowserSessionFixture(t)
			f.add(t, 0, 10, false)
			f.add(t, 1, 20, false)
			duplicate := f.add(t, 2, 10, false)
			f.cookies = append(f.cookies, consoleSessionCookieName(3)+"="+duplicate+".secret")
			otherDevice := f.add(t, -1, 10, false)
			forged := f.add(t, -2, 30, false)
			f.cookies = append(f.cookies, consoleSessionCookieName(4)+"="+forged+".wrong-secret")
			h := server.New(server.WithHostPorts("127.0.0.1:0"))
			path := "/api/v2/console/accounts/10"
			if logout {
				path = "/api/v2/console/session"
				h.DELETE(path, f.service.consoleAuth(true), f.service.deleteConsoleSession)
			} else {
				h.DELETE("/api/v2/console/accounts/:agent_id", f.service.consoleAuth(true), f.service.removeConsoleAccount)
			}
			status, payload, cookies := performJSON(t, h, http.MethodDelete, path, map[string]interface{}{},
				ut.Header{Key: "Cookie", Value: f.cookieHeader(2)}, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
			if status != http.StatusOK || responseData(t, payload)["active_agent_id"] != "20" {
				t.Fatalf("account removal did not switch to the distinct account: status=%d payload=%#v", status, payload)
			}
			for _, slot := range []int{0, 2, 3} {
				for _, cookieName := range []string{consoleSessionCookieName(slot), consoleCSRFCookieName(slot)} {
					found := false
					for _, cookie := range cookies {
						if strings.Contains(string(cookie), cookieName+"=; max-age=0;") {
							found = true
						}
					}
					if !found {
						t.Fatalf("cookie %s was not cleared: %q", cookieName, cookies)
					}
				}
			}
			for _, sessionID := range []string{"session-20-1", otherDevice, forged} {
				var sessionStatus string
				if err := f.service.db.Raw(`SELECT status FROM console_v2_sessions WHERE session_id = ?`, sessionID).Scan(&sessionStatus).Error; err != nil {
					t.Fatal(err)
				}
				if sessionStatus != "active" {
					t.Fatalf("unrelated or unproven session %q was revoked", sessionID)
				}
			}
			accounts := f.service.consoleAccounts(f.service.db, f.context(1), f.now)
			if len(accounts) != 1 || accounts[0].AgentID != "20" {
				t.Fatalf("removed account reappeared through a duplicate slot: %#v", accounts)
			}
		})
	}
}

func TestRemoveConsoleAccountUsesAuthenticatedFallbackSlot(t *testing.T) {
	f := newBrowserSessionFixture(t)
	f.add(t, 0, 20, true)
	f.add(t, 1, 10, false)
	f.add(t, 2, 30, false)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.DELETE("/api/v2/console/accounts/:agent_id", f.service.consoleAuth(true), f.service.removeConsoleAccount)
	status, payload, _ := performJSON(t, h, http.MethodDelete, "/api/v2/console/accounts/10", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: f.cookieHeader(0)}, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
	if status != http.StatusOK || responseData(t, payload)["active_agent_id"] != "30" {
		t.Fatalf("removal reported a revoked fallback account as active: status=%d payload=%#v", status, payload)
	}
}

func TestActivateConsoleAccountUsesValidDuplicateSession(t *testing.T) {
	f := newBrowserSessionFixture(t)
	f.add(t, 0, 10, true)
	f.add(t, 1, 20, false)
	f.add(t, 2, 10, false)
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/api/v2/console/accounts/:agent_id/activate", f.service.consoleAuth(true), f.service.activateConsoleAccount)
	status, payload, _ := performJSON(t, h, http.MethodPost, "/api/v2/console/accounts/10/activate", map[string]interface{}{},
		ut.Header{Key: "Cookie", Value: f.cookieHeader(1)}, ut.Header{Key: "X-CSRF-Token", Value: "csrf"})
	if status != http.StatusOK || responseData(t, payload)["slot"] != float64(2) {
		t.Fatalf("activation selected expired duplicate: status=%d payload=%#v", status, payload)
	}
	var sessionCount int64
	if err := f.service.db.Table("console_v2_sessions").Count(&sessionCount).Error; err != nil || sessionCount != 3 {
		t.Fatalf("activation unexpectedly created a session: count=%d err=%v", sessionCount, err)
	}
}
