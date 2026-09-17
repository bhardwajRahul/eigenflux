package skills

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
)

var errRecoveryRequired = errors.New("skills sync: interrupted installation requires recovery")

// readSyncSnapshot validates files between two reads of directory identity and
// manifest contents. A swap or metadata refresh invalidates the entire attempt.
// It never creates a lock, directory, marker, or timestamp.
func readSyncSnapshot(real string) (*Manifest, bool, error) {
	for attempt := 0; attempt < 3; attempt++ {
		before, err := inspectSyncSnapshot(real)
		if err != nil {
			return nil, false, err
		}
		intact := before.manifest != nil && !before.stale && len(before.manifest.Skills) > 0 &&
			verifyInstalledSkills(real, before.manifest) == nil
		after, err := inspectSyncSnapshot(real)
		if err != nil {
			return nil, false, err
		}
		if before.matches(after) {
			return before.manifest, intact, nil
		}
	}
	return nil, false, fmt.Errorf("skills sync: installation changed while being verified; retry sync")
}

type syncSnapshot struct {
	dir      os.FileInfo
	metadata os.FileInfo
	manifest *Manifest
	stale    bool
}

func inspectSyncSnapshot(real string) (*syncSnapshot, error) {
	if _, err := os.Lstat(real + journalSuffix); !os.IsNotExist(err) {
		if err != nil {
			return nil, err
		}
		return nil, errRecoveryRequired
	}
	dir, err := os.Stat(real)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	metadata, err := os.Stat(filepath.Join(real, ManifestFileName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	manifest, err := ReadLocalManifest(real)
	if err != nil {
		return nil, err
	}
	_, err = os.Lstat(filepath.Join(real, StaleMarkerName))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return &syncSnapshot{dir: dir, metadata: metadata, manifest: manifest, stale: err == nil}, nil
}

func (s *syncSnapshot) matches(other *syncSnapshot) bool {
	sameFile := func(a, b os.FileInfo) bool {
		if a == nil || b == nil {
			return a == nil && b == nil
		}
		return os.SameFile(a, b) && a.ModTime() == b.ModTime() && a.Size() == b.Size()
	}
	return sameFile(s.dir, other.dir) && sameFile(s.metadata, other.metadata) &&
		s.stale == other.stale && reflect.DeepEqual(s.manifest, other.manifest)
}
