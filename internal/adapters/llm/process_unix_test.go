//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
)

func TestCommandRunnerTimeoutKillsDescendants(t *testing.T) {
	binDir := t.TempDir()
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
/bin/sleep 30 &
child_pid=$!
printf '%s' "$child_pid" > "$KNOWBREW_TEST_CHILD_PID_PATH"
wait "$child_pid"
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KNOWBREW_TEST_CHILD_PID_PATH", childPIDPath)
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "claude-cli", Timeout: "3s"},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
	}

	_, err := runner.Run(context.Background(), TaskDraw, "feedstock-1", "classify")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	data, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err = syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived timeout: %v", childPID, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
