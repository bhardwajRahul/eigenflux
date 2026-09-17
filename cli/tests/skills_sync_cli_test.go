package tests

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cli.eigenflux.ai/internal/skills"
)

// Exercise the actual CLI boundary, including quiet exit status and structured
// output, against a signed local CDN and an installation with no write access.
func TestSkillsSyncCLIReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX directory permissions")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "build", "eigenflux")
	build := exec.Command("go", "build", "-ldflags", "-X main.Version=1.0.0 -X cli.eigenflux.ai/internal/skills.VerifyPublicKeyBase64="+base64.StdEncoding.EncodeToString(publicKey), "-o", binary, ".")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	old, oldArchive := signedRelease(t, privateKey, 100, "old content")
	updated, newArchive := signedRelease(t, privateKey, 101, "new content")
	oldManifest, _ := json.Marshal(old)
	newManifest, _ := json.Marshal(updated)
	var update, delayNextTar atomic.Bool
	downloadStarted := make(chan struct{})
	releaseDownload := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(releaseDownload) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifest, archive := oldManifest, oldArchive
		if update.Load() {
			manifest, archive = newManifest, newArchive
		}
		switch filepath.Base(r.URL.Path) {
		case skills.RemoteManifest:
			_, _ = w.Write(manifest)
		case skills.TarName:
			if delayNextTar.CompareAndSwap(true, false) {
				close(downloadStarted)
				<-releaseDownload
			}
			_, _ = w.Write(archive)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer unblock()
	targetParent := filepath.Join(root, "agent-skills")
	target := filepath.Join(targetParent, "skills")
	run := func(target string) ([]byte, error) {
		cmd := exec.Command(binary, "skills", "sync", "--into", target, "--quiet", "--if-stale", "--format", "json")
		// Avoid the developer's Agent Home and credentials entirely.
		cmd.Env = append(os.Environ(), "HOME="+root, "EIGENFLUX_HOME="+filepath.Join(root, "agent-home"), "EIGENFLUX_CDN_URL="+server.URL, "EIGENFLUX_SKILLS_DIR="+target)
		return cmd.CombinedOutput()
	}
	if output, err := run(target); err != nil {
		t.Fatalf("initial CLI install: %v\n%s", err, output)
	}
	for _, dir := range []string{targetParent, target} {
		if err := os.Chmod(dir, 0500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0700) })
	}
	probe := filepath.Join(targetParent, "probe")
	if err := os.WriteFile(probe, nil, 0600); err == nil {
		_ = os.Remove(probe)
		t.Skip("filesystem does not enforce read-only directories")
	}
	output, err := run(target)
	if err != nil || !strings.Contains(string(output), `"verified_manifest": true`) {
		t.Fatalf("read-only unchanged CLI sync: %v\n%s", err, output)
	}
	if _, err := os.Lstat(filepath.Join(targetParent, ".ef-skills.lock")); !os.IsNotExist(err) {
		t.Fatalf("unchanged CLI sync created a lock: %v", err)
	}
	update.Store(true)
	output, err = run(target)
	if err == nil || !strings.Contains(strings.ToLower(string(output)), "permission denied") {
		t.Fatalf("quiet CLI hid write failure: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "%!w") || strings.Contains(string(output), "panic:") {
		t.Fatalf("CLI lost original error: %s", output)
	}
	content, err := os.ReadFile(filepath.Join(target, "ef-profile", "SKILL.md"))
	if err != nil || string(content) != "old content" {
		t.Fatalf("failed CLI update damaged installation: %q, %v", content, err)
	}

	// Two separate CLI processes overlap their downloads. The second commits
	// while the first is blocked on the CDN; the first then skips its commit.
	concurrentTarget := filepath.Join(root, "concurrent", "skills")
	type outcome struct {
		output []byte
		err    error
	}
	firstDone := make(chan outcome, 1)
	delayNextTar.Store(true)
	go func() {
		output, err := run(concurrentTarget)
		firstDone <- outcome{output, err}
	}()
	select {
	case <-downloadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first CLI did not start download")
	}
	output, err = run(concurrentTarget)
	if err != nil || !strings.Contains(string(output), `"atomic": true`) {
		t.Fatalf("second CLI could not commit during first download: %v\n%s", err, output)
	}
	unblock()
	select {
	case first := <-firstDone:
		if first.err != nil || !strings.Contains(string(first.output), `"atomic": false`) || !strings.Contains(string(first.output), `"verified_manifest": true`) {
			t.Fatalf("first CLI failed to skip duplicate commit: %v\n%s", first.err, first.output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first CLI did not finish")
	}
}
