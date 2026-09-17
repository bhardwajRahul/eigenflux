package skills

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type syncTestTransport func(*http.Request) (*http.Response, error)

func (f syncTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func installedSyncFixture(t *testing.T) (SyncOptions, *Manifest, string) {
	t.Helper()
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "original"}})
	server, manifest := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 100)
	opts := syncOpts(filepath.Join(t.TempDir(), "skills"), "1.0.0", server.URL, nil)
	if _, err := Sync(opts); err != nil {
		t.Fatal(err)
	}
	return opts, manifest, src
}

func makeSyncDirReadOnly(t *testing.T, dir string) {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, info.Mode().Perm()) })
	probe := filepath.Join(dir, ".permission-probe")
	if err := os.WriteFile(probe, nil, 0600); err == nil {
		_ = os.Remove(probe)
		t.Skip("filesystem does not enforce read-only directory permissions")
	} else if !errors.Is(err, os.ErrPermission) {
		t.Fatal(err)
	}
}

func TestSyncUnchangedReadOnlyDoesNotWrite(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	parent := filepath.Dir(opts.Into)
	makeSyncDirReadOnly(t, opts.Into)
	makeSyncDirReadOnly(t, parent)
	before, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	manifestBefore, _ := os.ReadFile(filepath.Join(opts.Into, ManifestFileName))
	opts.Quiet, opts.IfStale = true, true
	res, err := Sync(opts)
	if err != nil || res == nil || !res.VerifiedManifest || res.Atomic {
		t.Fatalf("read-only no-change sync: %+v, %v", res, err)
	}
	after, _ := os.Stat(parent)
	manifestAfter, _ := os.ReadFile(filepath.Join(opts.Into, ManifestFileName))
	if !before.ModTime().Equal(after.ModTime()) || !bytes.Equal(manifestBefore, manifestAfter) {
		t.Fatal("no-change check modified installation metadata")
	}
	if _, err := os.Lstat(filepath.Join(parent, lockFileName)); !os.IsNotExist(err) {
		t.Fatalf("no-change check created lock: %v", err)
	}
}

func TestSyncNoChangeCanReadWhileLockHeld(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	lock, ok, err := acquireLock(filepath.Dir(opts.Into))
	if err != nil || !ok {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Release()
	res, err := Sync(opts)
	if err != nil || res == nil || !res.VerifiedManifest {
		t.Fatalf("intact unchanged snapshot should not acquire lock: %+v, %v", res, err)
	}
}

func TestSyncFailedFreshCheckDoesNotCreateDirectories(t *testing.T) {
	target := filepath.Join(t.TempDir(), "absent", "skills")
	res, err := Sync(SyncOptions{Into: target, Quiet: true, CDNBase: "http://127.0.0.1:1"})
	if err == nil || res != nil {
		t.Fatalf("quiet hid first-install failure: %+v, %v", res, err)
	}
	if _, err := os.Stat(filepath.Dir(target)); !os.IsNotExist(err) {
		t.Fatalf("failed check created parent: %v", err)
	}
}

func TestSyncReadOnlyUpdateReportsPermissionFailure(t *testing.T) {
	for _, metadataOnly := range []bool{false, true} {
		name := "content"
		if metadataOnly {
			name = "metadata"
		}
		t.Run(name, func(t *testing.T) {
			opts, _, src := installedSyncFixture(t)
			if !metadataOnly {
				src = stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "updated"}})
			}
			server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 101)
			opts.CDNBase, opts.Quiet = server.URL, true
			makeSyncDirReadOnly(t, opts.Into)
			if !metadataOnly {
				makeSyncDirReadOnly(t, filepath.Dir(opts.Into))
			}
			before, _ := os.ReadFile(filepath.Join(opts.Into, ManifestFileName))
			res, err := Sync(opts)
			if !errors.Is(err, os.ErrPermission) || res != nil {
				t.Fatalf("update should report permission failure: %+v, %v", res, err)
			}
			after, _ := os.ReadFile(filepath.Join(opts.Into, ManifestFileName))
			if !bytes.Equal(before, after) {
				t.Fatal("failed update changed manifest")
			}
			content, _ := os.ReadFile(filepath.Join(opts.Into, "ef-broadcast", "SKILL.md"))
			if string(content) != "original" {
				t.Fatal("failed update changed content")
			}
		})
	}
}

func TestSyncSameContentAdvancesSequenceWithoutDownload(t *testing.T) {
	opts, _, src := installedSyncFixture(t)
	server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 101)
	oldURL := opts.CDNBase
	opts.CDNBase = server.URL
	opts.HTTPClient = &http.Client{Transport: syncTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, TarName) {
			t.Error("metadata-only refresh downloaded archive")
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	res, err := Sync(opts)
	if err != nil || res == nil || !res.VerifiedManifest || res.Atomic {
		t.Fatalf("metadata refresh: %+v, %v", res, err)
	}
	manifest, _ := ReadLocalManifest(opts.Into)
	if manifest.Sequence != 101 {
		t.Fatalf("lost sequence advancement: %+v", manifest)
	}
	opts.CDNBase = oldURL
	res, err = Sync(opts)
	if err != nil || res == nil || res.VerifiedManifest || res.Atomic {
		t.Fatalf("rollback accepted: %+v, %v", res, err)
	}
	manifest, _ = ReadLocalManifest(opts.Into)
	if manifest.Sequence != 101 {
		t.Fatal("rollback lowered sequence")
	}
}

func TestSyncRechecksInstallationAfterConcurrentDownload(t *testing.T) {
	for _, newer := range []bool{false, true} {
		name := "same-release"
		if newer {
			name = "newer-release"
		}
		t.Run(name, func(t *testing.T) {
			opts, _, _ := installedSyncFixture(t)
			src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "next"}})
			server, remote := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 101)
			opts.CDNBase = server.URL
			other := opts
			want := remote
			if newer {
				latest := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "newest"}})
				latestServer, latestManifest := serveBundleAtSequence(t, "1.0.0", latest, []string{"ef-broadcast"}, 102)
				other.CDNBase, want = latestServer.URL, latestManifest
			}
			var committed *SyncResult
			opts.HTTPClient = &http.Client{Transport: syncTestTransport(func(r *http.Request) (*http.Response, error) {
				if strings.HasSuffix(r.URL.Path, TarName) {
					// A second writer can finish while the first is downloading.
					var err error
					committed, err = Sync(other)
					if err != nil || committed == nil || !committed.Atomic {
						t.Fatalf("download held installation lock: %+v, %v", committed, err)
					}
				}
				return http.DefaultTransport.RoundTrip(r)
			})}
			res, err := Sync(opts)
			if err != nil || res == nil || res.Atomic || committed == nil {
				t.Fatalf("duplicate/stale commit: %+v, %v", res, err)
			}
			got, _ := ReadLocalManifest(opts.Into)
			if got.Sequence != want.Sequence || got.Revision != want.Revision {
				t.Fatalf("concurrent release overwritten: %+v; want %+v", got, want)
			}
		})
	}
}

func TestSyncPreservesEditsMadeDuringDownload(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "next"}})
	server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 101)
	opts.CDNBase = server.URL
	file := filepath.Join(opts.Into, "ef-broadcast", "SKILL.md")
	opts.HTTPClient = &http.Client{Transport: syncTestTransport(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, TarName) {
			if err := os.WriteFile(file, []byte("user edit"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	res, err := Sync(opts)
	if err != nil || res == nil || len(res.Preserved) != 1 || res.VerifiedManifest {
		t.Fatalf("concurrent user edit was not preserved: %+v, %v", res, err)
	}
	content, _ := os.ReadFile(file)
	if string(content) != "user edit" {
		t.Fatal("user edit overwritten")
	}
}

func TestSyncRecoversJournalBeforeNoChange(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	old := opts.Into + oldSuffix
	if err := os.Rename(opts.Into, old); err != nil {
		t.Fatal(err)
	}
	if err := writeJournal(opts.Into+journalSuffix, old); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(opts)
	if err != nil || res == nil || !res.VerifiedManifest || res.Atomic {
		t.Fatalf("interrupted installation not recovered: %+v, %v", res, err)
	}
	if _, err := os.Lstat(opts.Into + journalSuffix); !os.IsNotExist(err) {
		t.Fatalf("journal not cleared: %v", err)
	}
}

func TestSyncActiveJournalNeverReturnsVerifiedSnapshot(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	lock, ok, err := acquireLock(filepath.Dir(opts.Into))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer lock.Release()
	if err := writeJournal(opts.Into+journalSuffix, opts.Into+oldSuffix); err != nil {
		t.Fatal(err)
	}
	res, err := Sync(opts)
	if err == nil || res != nil {
		t.Fatalf("active transaction treated as valid: %+v, %v", res, err)
	}
	if _, err := os.Stat(opts.Into + journalSuffix); err != nil {
		t.Fatalf("reader recovered another writer's journal: %v", err)
	}
}

func TestSyncOfflineRefusesDamagedInstallation(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	if err := os.Remove(filepath.Join(opts.Into, "ef-broadcast", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	opts.CDNBase, opts.Quiet, opts.IfStale = "http://127.0.0.1:1", true, true
	res, err := Sync(opts)
	if err == nil || res != nil {
		t.Fatalf("damaged offline installation accepted: %+v, %v", res, err)
	}
}

func TestSyncSnapshotDetectsIdenticalDirectoryReplacement(t *testing.T) {
	opts, _, _ := installedSyncFixture(t)
	before, err := inspectSyncSnapshot(opts.Into)
	if err != nil {
		t.Fatal(err)
	}
	old := opts.Into + oldSuffix
	if err := os.Rename(opts.Into, old); err != nil {
		t.Fatal(err)
	}
	if err := copyDir(old, opts.Into); err != nil {
		t.Fatal(err)
	}
	after, err := inspectSyncSnapshot(opts.Into)
	if err != nil {
		t.Fatal(err)
	}
	if before.matches(after) {
		t.Fatal("same-content directory replacement was not detected")
	}
}

func TestRecoverInterruptedFailurePreservesRollback(t *testing.T) {
	real := filepath.Join(t.TempDir(), "skills")
	old := real + oldSuffix
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real, []byte("obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeJournal(real+journalSuffix, old); err != nil {
		t.Fatal(err)
	}
	if err := recoverInterrupted(real); err == nil {
		t.Fatal("failed rollback was ignored")
	}
	if !dirExists(old) || !fileExists(real+journalSuffix) {
		t.Fatal("failed rollback destroyed recovery state")
	}
}

func TestSyncRepairsMissingSkillAndProvisionalInstall(t *testing.T) {
	for _, provisional := range []bool{false, true} {
		name := "missing-skill"
		if provisional {
			name = "provisional"
		}
		t.Run(name, func(t *testing.T) {
			opts, _, _ := installedSyncFixture(t)
			if provisional {
				if err := os.WriteFile(filepath.Join(opts.Into, StaleMarkerName), []byte("provisional"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.RemoveAll(filepath.Join(opts.Into, "ef-broadcast")); err != nil {
				t.Fatal(err)
			}
			res, err := Sync(opts)
			if err != nil || res == nil || !res.Atomic || !res.VerifiedManifest {
				t.Fatalf("repair failed: %+v, %v", res, err)
			}
			manifest, intact, err := readSyncSnapshot(opts.Into)
			if err != nil || !intact || manifest == nil {
				t.Fatalf("repair left invalid installation: %+v, %v", manifest, err)
			}
		})
	}
}

func TestSyncBusyFreshInstallReturnsError(t *testing.T) {
	src := stageSkills(t, map[string]map[string]string{"ef-broadcast": {"SKILL.md": "fresh"}})
	server, _ := serveBundleAtSequence(t, "1.0.0", src, []string{"ef-broadcast"}, 100)
	parent := t.TempDir()
	lock, ok, err := acquireLock(parent)
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer lock.Release()
	opts := syncOpts(filepath.Join(parent, "skills"), "1.0.0", server.URL, nil)
	opts.Quiet = true
	res, err := Sync(opts)
	if err == nil || res != nil || dirExists(opts.Into) {
		t.Fatalf("busy first install reported nonexistent local copy: %+v, %v", res, err)
	}
}
