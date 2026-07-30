package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/siro33950/knowbrew/internal/config"
)

func TestCommandRunnerTimesOut(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
echo "backend is waiting" >&2
exec /bin/sleep 5
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "claude-cli", Timeout: "500ms"},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
	}

	started := time.Now()
	err := runner.Run(context.Background(), TaskAnnotate, "claude-session-t000001", "classify")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("timeout took %s", elapsed)
	}
}

func TestCommandRunnerCapturesNonTTYDiagnostics(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
echo "stdout detail"
echo "stderr detail" >&2
exit 7
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "claude-cli", Timeout: "5s"},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
		Progress:   nil,
	}

	err := runner.Run(context.Background(), TaskAnnotate, "claude-session-t000001", "classify")
	if err == nil {
		t.Fatal("expected backend failure")
	}
	for _, want := range []string{"claude-cli failed", "stderr detail", "stdout detail"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestClippedAPIPreservesUTF8(t *testing.T) {
	output := clippedAPI([]byte(strings.Repeat("日本語", 1000)))
	if !utf8.ValidString(output) {
		t.Fatalf("clipped API response is invalid UTF-8")
	}
	if !strings.HasSuffix(output, "…") {
		t.Fatalf("clipped API response has no ellipsis")
	}
	if len(output) > 1000+len("…") {
		t.Fatalf("clipped API response is %d bytes", len(output))
	}
}

func TestClaudeBrewPermissionsAllowReadsAndKnowledgeWritesOnly(t *testing.T) {
	permissions := strings.Join(claudeAllowedTools("/bin/knowbrew", TaskBrew), "\n")
	for _, required := range []string{
		"/bin/knowbrew knowledge create *",
		"/bin/knowbrew knowledge add-source *",
		"/bin/knowbrew knowledge invalidate *",
		"/bin/knowbrew knowledge --include-pending *",
		"/bin/knowbrew feedstock -- *",
		"/bin/knowbrew show *",
	} {
		if !strings.Contains(permissions, required) {
			t.Fatalf("permissions do not contain %q:\n%s", required, permissions)
		}
	}
	if strings.Contains(permissions, "feedstock annotate") {
		t.Fatalf("brew permissions expose feedstock annotation:\n%s", permissions)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
