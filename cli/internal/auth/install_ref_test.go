package auth_test

import (
	"os"
	"path/filepath"
	"testing"

	"cli.eigenflux.ai/internal/auth"
	"cli.eigenflux.ai/internal/config"
)

func TestInstallRefSurvivesProvisionAndReinstall(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	server := config.Server{Name: "eigenflux", Endpoint: "https://www.eigenflux.ai"}
	record, saved, err := auth.RememberInstallRef(server, "https://eigenflux.ai/", " EF-Bili1234 ")
	if err != nil || !saved || record == nil || record.Ref != "EF-Bili1234" {
		t.Fatalf("initial install ref: record=%+v saved=%v err=%v", record, saved, err)
	}
	path := filepath.Join(config.HomeDir(), "servers", server.Name, "install-ref.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ref file permissions: info=%v err=%v", info, err)
	}
	if err := auth.SaveV2Credentials(server.Name, &auth.V2Credentials{AgentID: "42", AccessToken: "access", RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	record, saved, err = auth.RememberInstallRef(server, server.Endpoint, "EF-Other123")
	if err != nil || saved || record == nil || record.Ref != "EF-Bili1234" {
		t.Fatalf("reinstall replaced the original attribution: record=%+v saved=%v err=%v", record, saved, err)
	}
	record, err = auth.LoadInstallRef(server)
	if err != nil || record == nil || record.Ref != "EF-Bili1234" {
		t.Fatalf("retry lost the original ref: record=%+v err=%v", record, err)
	}
}

func TestInstallRefIsolatedByHomeServerAndEndpoint(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	server := config.Server{Name: "eigenflux", Endpoint: "https://www.eigenflux.ai"}
	if _, _, err := auth.RememberInstallRef(server, server.Endpoint, "EF-Bili1234"); err != nil {
		t.Fatal(err)
	}
	for _, other := range []config.Server{
		{Name: "staging", Endpoint: server.Endpoint},
		{Name: server.Name, Endpoint: "https://staging.eigenflux.ai"},
		{Name: server.Name, Endpoint: "http://www.eigenflux.ai"},
	} {
		if record, err := auth.LoadInstallRef(other); err != nil || record != nil {
			t.Fatalf("ref crossed server or endpoint boundary: server=%+v record=%+v err=%v", other, record, err)
		}
	}
	if _, _, err := auth.RememberInstallRef(server, "https://staging.eigenflux.ai", "EF-Bili1234"); err == nil {
		t.Fatal("mismatched source endpoint must be rejected")
	}
	for _, alias := range []string{"https://eigenflux.ai/", "https://www.eigenflux.net", "https://eigenflux.pro", "https://phronesis.studio"} {
		if record, err := auth.LoadInstallRef(config.Server{Name: server.Name, Endpoint: alias}); err != nil || record == nil || record.Ref != "EF-Bili1234" {
			t.Fatalf("official alias lost the referral: alias=%q record=%+v err=%v", alias, record, err)
		}
	}
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	if record, err := auth.LoadInstallRef(server); err != nil || record != nil {
		t.Fatalf("ref crossed Agent Home boundary: record=%+v err=%v", record, err)
	}
}

func TestInstallRefDoesNotAttributeExistingIdentities(t *testing.T) {
	for _, identity := range []string{"v2", "legacy", "expired_legacy"} {
		t.Run(identity, func(t *testing.T) {
			t.Setenv("EIGENFLUX_HOME", t.TempDir())
			server := config.Server{Name: "eigenflux", Endpoint: "https://www.eigenflux.ai"}
			var err error
			if identity == "v2" {
				err = auth.SaveV2Credentials(server.Name, &auth.V2Credentials{AgentID: "42", AccessToken: "access", RefreshToken: "refresh"})
			} else {
				creds := &auth.Credentials{AgentID: "42", AccessToken: "legacy"}
				if identity == "expired_legacy" {
					creds.ExpiresAt = 1
				}
				err = auth.SaveCredentials(server.Name, creds)
			}
			if err != nil {
				t.Fatal(err)
			}
			record, saved, err := auth.RememberInstallRef(server, server.Endpoint, "EF-Bili1234")
			if err != nil || saved || record != nil {
				t.Fatalf("existing identity gained attribution: record=%+v saved=%v err=%v", record, saved, err)
			}
		})
	}
}

func TestInstallRefRejectsMalformedReferral(t *testing.T) {
	t.Setenv("EIGENFLUX_HOME", t.TempDir())
	server := config.Server{Name: "eigenflux", Endpoint: "https://www.eigenflux.ai"}
	for _, ref := range []string{"", "bilibili", "EF-too-long123", "EF-Bili1234\nnext"} {
		if _, _, err := auth.RememberInstallRef(server, server.Endpoint, ref); err == nil {
			t.Errorf("invalid referral accepted: %q", ref)
		}
	}
}
