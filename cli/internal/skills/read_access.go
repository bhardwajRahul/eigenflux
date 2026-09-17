package skills

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// checkSyncReadAccess uses actual reads so ACLs and sandbox restrictions are
// honored. It is a preflight only; callers must still handle errors under lock.
// Missing installation paths are valid on first install.
func checkSyncReadAccess(real, parent string) error {
	for ancestor := parent; ; ancestor = filepath.Dir(ancestor) {
		if _, err := os.ReadDir(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("skills sync: cannot read installation parent before locking: %w", err)
		}
		if filepath.Dir(ancestor) == ancestor {
			break
		}
	}
	paths := []string{real}
	data, err := os.ReadFile(real + journalSuffix)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("skills sync: cannot read recovery journal before locking: %w", err)
	}
	if err == nil {
		if old := strings.TrimSpace(string(data)); old != "" {
			paths = append(paths, old)
		}
	}
	for _, root := range paths {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				// A concurrent swap can temporarily remove a path. The locked
				// reread decides whether it requires recovery or installation.
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if !entry.Type().IsRegular() {
				return fmt.Errorf("unsupported installation file: %s", path)
			}
			file, err := os.Open(path)
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			var probe [1]byte
			_, readErr := file.Read(probe[:])
			closeErr := file.Close()
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			return closeErr
		})
		if err != nil {
			return fmt.Errorf("skills sync: cannot read installation before locking: %w", err)
		}
	}
	return nil
}
