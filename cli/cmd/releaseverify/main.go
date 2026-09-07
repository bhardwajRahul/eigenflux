// releaseverify exercises the real sync path in a disposable directory.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"cli.eigenflux.ai/internal/skills"
)

func main() {
	manifestPath := flag.String("manifest", "", "expected signed manifest")
	cdn := flag.String("cdn", skills.CDNDefault, "published CDN base")
	bundle := flag.String("bundle", "", "verify a staged bundle before upload")
	flag.Parse()
	if *bundle != "" {
		mux := http.NewServeMux()
		for _, prefix := range []string{"/skills/latest", "/cli/latest"} {
			mux.Handle(prefix+"/", http.StripPrefix(prefix, http.FileServer(http.Dir(*bundle))))
		}
		server := httptest.NewServer(mux)
		defer server.Close()
		*cdn = server.URL
	}
	if err := verify(*manifestPath, *cdn); err != nil {
		log.Fatal(err)
	}
}

func verify(manifestPath, cdn string) error {
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var expected skills.Manifest
	if err := json.Unmarshal(data, &expected); err != nil {
		return err
	}
	if err := skills.ValidateSignedRelease(&expected); err != nil {
		return err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	for _, prefix := range []string{"skills/latest", "cli/latest"} {
		resp, err := client.Get(cdn + "/" + prefix + "/manifest.json")
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if readErr != nil {
			return readErr
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d", prefix, resp.StatusCode)
		}
		var live skills.Manifest
		if err := json.Unmarshal(body, &live); err != nil {
			return err
		}
		if err := skills.ValidateSignedRelease(&live); err != nil {
			return err
		}
		if live.Sequence != expected.Sequence || live.Signature != expected.Signature {
			return fmt.Errorf("%s: CDN does not serve the expected signed release", prefix)
		}
	}
	root, err := os.MkdirTemp("", "eigenflux-release-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	opts := skills.SyncOptions{Into: filepath.Join(root, "skills"), CLIVersion: expected.CLIVersion, CDNBase: cdn, HTTPClient: client}
	result, err := skills.Sync(opts)
	if err != nil {
		return err
	}
	if !result.VerifiedManifest || !result.Atomic || result.Source != "skills/latest" || len(result.Preserved) != 0 {
		return fmt.Errorf("atomic installation failed: %+v", result)
	}
	installed, err := skills.ReadLocalManifest(opts.Into)
	if err != nil {
		return err
	}
	if installed.Sequence != expected.Sequence || installed.Signature != expected.Signature {
		return fmt.Errorf("installed release differs from expected manifest")
	}
	fmt.Printf("verified atomic CDN installation: sequence=%d revision=%s\n", installed.Sequence, installed.Revision)
	result, err = skills.Sync(opts)
	if err != nil {
		return err
	}
	if !result.VerifiedManifest || result.Atomic || result.Source != "local" {
		return fmt.Errorf("repeat sync is not a verified no-op: %+v", result)
	}
	fmt.Println("verified repeat sync: source=local verified_manifest=true atomic=false")
	return nil
}
