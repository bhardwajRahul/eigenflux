package sanity_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRunnerPreservesCallerEnvironmentAndSelectsRootPackages(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("test runner requires bash")
	}
	script, err := os.ReadFile("../run.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		env  []string
		want []string
	}{
		{"dotenv_defaults", nil, []string{"file-database", "file-redis"}},
		{"explicit_stack", []string{"PG_DSN=isolated-database", "REDIS_ADDR=isolated-redis"}, []string{"isolated-database", "isolated-redis"}},
		{"explicit_empty", []string{"PG_DSN=", "REDIS_ADDR="}, []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{bin, filepath.Join(root, "tests")} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string][]byte{
				"tests/run.sh": script,
				".env":         []byte("PG_DSN=file-database\nREDIS_ADDR=file-redis\n"),
				"bin/go":       []byte("#!/bin/bash\nprintf '%s\\0' \"${PG_DSN-}\" \"${REDIS_ADDR-}\" \"$@\" > \"$RUNNER_RESULT\"\n"),
			}
			for name, contents := range files {
				if err := os.WriteFile(filepath.Join(root, name), contents, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			resultPath := filepath.Join(root, "result")
			cmd := exec.Command(bash, filepath.Join(root, "tests/run.sh"), "--skip-start")
			cmd.Dir = root
			cmd.Env = append([]string{"PATH=" + bin + ":/usr/bin:/bin", "RUNNER_RESULT=" + resultPath}, tc.env...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("test runner failed: %v\n%s", err, output)
			}
			result, err := os.ReadFile(resultPath)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Split(strings.TrimSuffix(string(result), "\x00"), "\x00")
			want := append(append([]string{}, tc.want...), "test", "-v", "-count=1", "-timeout", "30m", "./...")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("runner environment and arguments = %q, want %q", got, want)
			}
		})
	}
}
