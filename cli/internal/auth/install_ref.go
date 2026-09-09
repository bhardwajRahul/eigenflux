package auth

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cli.eigenflux.ai/internal/config"
)

var installRefPattern = regexp.MustCompile(`^EF-[0-9A-Za-z]{8}$`)

// InstallRef preserves the first installation referral for one server and Home.
// It remains after provisioning so retries sign the same attribution context.
type InstallRef struct {
	Ref      string `json:"ref"`
	Endpoint string `json:"endpoint"`
}

func installRefPath(serverName string) string {
	return filepath.Join(config.HomeDir(), "servers", serverName, "install-ref.json")
}

func canonicalInstallEndpoint(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid install referral endpoint")
	}
	u.Host = strings.ToLower(u.Host)
	if u.Scheme == "https" {
		switch u.Host {
		case "eigenflux.ai", "www.eigenflux.ai", "eigenflux.net", "www.eigenflux.net",
			"eigenflux.pro", "www.eigenflux.pro", "phronesis.studio", "www.phronesis.studio":
			u.Host = "www.eigenflux.ai"
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// LoadInstallRef returns only a referral belonging to the selected endpoint.
func LoadInstallRef(server config.Server) (*InstallRef, error) {
	data, err := os.ReadFile(installRefPath(server.Name))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record InstallRef
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("parse install referral: %w", err)
	}
	if !installRefPattern.MatchString(record.Ref) {
		return nil, fmt.Errorf("invalid saved install referral")
	}
	endpoint, err := canonicalInstallEndpoint(server.Endpoint)
	if err != nil {
		return nil, err
	}
	storedEndpoint, err := canonicalInstallEndpoint(record.Endpoint)
	if err != nil {
		return nil, err
	}
	if storedEndpoint != endpoint {
		return nil, nil
	}
	return &record, nil
}

// RememberInstallRef saves once before an identity exists. Existing referrals
// take precedence, and existing V2 or legacy identities never gain a new ref.
func RememberInstallRef(server config.Server, sourceEndpoint, ref string) (*InstallRef, bool, error) {
	ref = strings.TrimSpace(ref)
	if !installRefPattern.MatchString(ref) {
		return nil, false, fmt.Errorf("install referral must match EF- followed by 8 letters or digits")
	}
	endpoint, err := canonicalInstallEndpoint(server.Endpoint)
	if err != nil {
		return nil, false, err
	}
	source, err := canonicalInstallEndpoint(sourceEndpoint)
	if err != nil {
		return nil, false, err
	}
	if source != endpoint {
		return nil, false, fmt.Errorf("install referral endpoint does not match selected server %q", server.Name)
	}
	var record *InstallRef
	saved := false
	err = WithV2CredentialsLock(server.Name, 5*time.Second, func() error {
		var err error
		record, err = LoadInstallRef(server)
		if err != nil || record != nil {
			return err
		}
		hasV2, err := HasV2Credentials(server.Name)
		if err != nil || hasV2 {
			return err
		}
		if _, err := os.Lstat(credentialsPath(server.Name)); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		record = &InstallRef{Ref: ref, Endpoint: endpoint}
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(installRefPath(server.Name), data); err != nil {
			return err
		}
		saved = true
		return nil
	})
	return record, saved, err
}
