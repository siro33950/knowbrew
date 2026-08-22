package llm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/siro33950/knowbrew/internal/adapters/config"
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
	_, err := runner.Run(context.Background(), TaskDraw, "claude-session-t000001", "classify")
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
	var progress bytes.Buffer
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "claude-cli", Timeout: "60s"},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
		Progress:   &progress,
	}

	_, err := runner.Run(context.Background(), TaskDraw, "claude-session-t000001", "classify")
	if err == nil {
		t.Fatal("expected backend failure")
	}
	for _, want := range []string{"claude-cli failed", "stderr detail", "stdout detail"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	if progress.Len() != 0 {
		t.Fatalf("backend output leaked into progress:\n%s", progress.String())
	}
}

func TestCommandRunnerRejectsMissingStructuredResult(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "codex"), `#!/bin/sh
echo '{"type":"item.completed","item":{"text":"agent stopped before writing"}}'
echo "backend warning" >&2
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{Backend: "codex-cli", Timeout: "5s"},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
	}

	_, err := runner.Run(context.Background(), TaskDraw, "feedstock-1", "classify")
	if err == nil {
		t.Fatal("expected a missing-result failure")
	}
	for _, required := range []string{"invalid structured JSON"} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("error %q does not contain %q", err, required)
		}
	}
}

func TestCommandRunnerRemovesEchoedPromptFromFailure(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
printf '%s\n' "$2" >&2
echo "ERROR: Selected model is at capacity." >&2
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
	}
	prompt := "SECRET MULTILINE PROMPT\nthat must not appear in diagnostics"
	_, err := runner.Run(context.Background(), TaskDraw, "feedstock-1", prompt)
	if err == nil {
		t.Fatal("expected backend failure")
	}
	if strings.Contains(err.Error(), prompt) || strings.Contains(err.Error(), "SECRET MULTILINE PROMPT") {
		t.Fatalf("failure contains the echoed prompt:\n%s", err)
	}
	if !strings.Contains(err.Error(), "ERROR: Selected model is at capacity.") {
		t.Fatalf("failure lost the substantive error:\n%s", err)
	}
}

func TestCommandDiagnosticRemovesPromptEcho(t *testing.T) {
	prompt := "classify exactly one record\nwith these closed vocabularies"
	got := commandDiagnostic(
		"codex metadata\n"+prompt+"\nERROR: Selected model is at capacity.",
		"",
		prompt,
	)
	if strings.Contains(got, prompt) {
		t.Fatalf("diagnostic contains the sent prompt:\n%s", got)
	}
	for _, required := range []string{"codex metadata", "ERROR: Selected model is at capacity."} {
		if !strings.Contains(got, required) {
			t.Fatalf("diagnostic does not contain %q:\n%s", required, got)
		}
	}
}

func TestCompactDiagnosticKeepsOnlyBoundedTail(t *testing.T) {
	lines := make([]string, 30)
	for index := range lines {
		lines[index] = fmt.Sprintf("line-%02d %s", index, strings.Repeat("x", 120))
	}
	got := compactDiagnostic(strings.Join(lines, "\n"), "")
	if !strings.Contains(got, diagnosticTruncatedMarker) {
		t.Fatalf("truncation is not marked:\n%s", got)
	}
	if count := len(strings.Split(got, "\n")); count > diagnosticMaxLines {
		t.Fatalf("diagnostic has %d lines, want at most %d", count, diagnosticMaxLines)
	}
	if count := len([]rune(got)); count > diagnosticMaxRunes {
		t.Fatalf("diagnostic has %d characters, want at most %d", count, diagnosticMaxRunes)
	}
	if strings.Contains(got, "line-00") || !strings.Contains(got, "line-29") {
		t.Fatalf("diagnostic did not preserve only the tail:\n%s", got)
	}
}

func TestCommandDiagnosticOmitsDuplicateStdout(t *testing.T) {
	for _, test := range []struct {
		name   string
		stderr string
		stdout string
	}{
		{name: "identical", stderr: "ERROR: capacity", stdout: "ERROR: capacity"},
		{name: "contained", stderr: "context\nERROR: capacity", stdout: "ERROR: capacity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := commandDiagnostic(test.stderr, test.stdout, "")
			if strings.Contains(got, "stdout:") {
				t.Fatalf("duplicate stdout was included:\n%s", got)
			}
			if strings.Count(got, "ERROR: capacity") != 1 {
				t.Fatalf("diagnostic duplicated the error:\n%s", got)
			}
		})
	}
}

func TestCommandDiagnosticHasNoEmptyHeadings(t *testing.T) {
	prompt := "prompt only"
	if got := commandDiagnostic(prompt, prompt, prompt); got != "" {
		t.Fatalf("prompt-only diagnostic = %q, want empty", got)
	}
}

func TestCommandRunnerStreamsBackendOutputOnlyWhenVerbose(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
printf '%s\n' '{"structured_output":{"types":[]},"marker":"backend stdout marker"}'
echo "backend stderr marker" >&2
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, test := range []struct {
		name    string
		verbose bool
	}{
		{name: "quiet"},
		{name: "verbose", verbose: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("INVOCATION_ROOT", root)
			var progress bytes.Buffer
			runner := &CommandRunner{
				Config: config.Config{
					Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
					LLM: config.LLM{Backend: "claude-cli", Timeout: "5s"},
				},
				Executable: filepath.Join(root, "knowbrew"),
				WorkDir:    root,
				Progress:   &progress,
				Verbose:    test.verbose,
			}
			if _, err := runner.Run(
				context.Background(),
				TaskDraw,
				"claude-session-t000001",
				"classify",
			); err != nil {
				t.Fatal(err)
			}
			containsOutput := strings.Contains(progress.String(), "backend stdout marker") &&
				strings.Contains(progress.String(), "backend stderr marker")
			if containsOutput != test.verbose {
				t.Fatalf("verbose=%v progress:\n%s", test.verbose, progress.String())
			}
		})
	}
}

func TestCommandRunnerReportsBackendUsage(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
printf '%s\n' '{"type":"result","structured_output":{"types":[]},"usage":{"input_tokens":100,"cache_creation_input_tokens":200,"cache_read_input_tokens":300,"output_tokens":40}}'
`)
	writeExecutable(t, filepath.Join(binDir, "codex"), `#!/bin/sh
result_path=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then result_path="$2"; shift 2; continue; fi
  shift
done
printf '%s\n' '{"types":[]}' > "$result_path"
printf '%s\n' '{"type":"thread.started","thread_id":"thread-1"}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":450,"cached_input_tokens":300,"output_tokens":50}}'
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, test := range []struct {
		backend string
		want    Usage
	}{
		{
			backend: "claude-cli",
			want: Usage{
				InputTokens: 600, CachedInputTokens: 300,
				CacheWriteInputTokens: 200, OutputTokens: 40,
			},
		},
		{
			backend: "codex-cli",
			want:    Usage{InputTokens: 450, CachedInputTokens: 300, OutputTokens: 50},
		},
	} {
		t.Run(test.backend, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("INVOCATION_ROOT", root)
			runner := &CommandRunner{
				Config: config.Config{
					Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
					LLM: config.LLM{Backend: test.backend, Timeout: "5s"},
				},
				Executable: filepath.Join(root, "knowbrew"),
				WorkDir:    root,
			}
			usage, err := runner.RunWithUsage(
				context.Background(),
				TaskDraw,
				"feedstock-1",
				"classify",
			)
			if err != nil {
				t.Fatal(err)
			}
			if usage != test.want {
				t.Fatalf("usage = %#v, want %#v", usage, test.want)
			}
		})
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

func TestClaudeExtractAndBrewExposeNoTools(t *testing.T) {
	for _, task := range []Task{TaskExtract, TaskBrew} {
		if allowed := claudeAllowedTools("/bin/knowbrew", task); len(allowed) != 0 {
			t.Fatalf("%s permissions = %#v", task, allowed)
		}
	}
}

func TestClaudeDrawPermissionsAllowOnlyBoundedContext(t *testing.T) {
	permissions := strings.Join(claudeAllowedTools("/bin/knowbrew", TaskDraw), "\n")
	for _, required := range []string{
		"/bin/knowbrew feedstock context *",
	} {
		if !strings.Contains(permissions, required) {
			t.Fatalf("permissions do not contain %q:\n%s", required, permissions)
		}
	}
	for _, forbidden := range []string{"feedstock draft", "show", "knowledge create", "knowledge invalidate", "feedstock --"} {
		if strings.Contains(permissions, forbidden) {
			t.Fatalf("draw permissions contain %q:\n%s", forbidden, permissions)
		}
	}
}

func TestClaudeDistillPermissionsExposeNoTools(t *testing.T) {
	for _, task := range []Task{TaskDistillSelect, TaskDistillGenerate} {
		permissions := strings.Join(claudeAllowedTools("/bin/knowbrew", task), "\n")
		if permissions != "" {
			t.Fatalf("%s permissions = %q, want no tools", task, permissions)
		}
	}
}

func TestClaudeCommandRunnerUsesTaskSpecificModelAndEffortArguments(t *testing.T) {
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "arguments.txt")
	writeExecutable(t, filepath.Join(binDir, "claude"), `#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_PATH"
exit 9
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capturePath)
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{
				Backend: "claude-cli", DrawModel: "draw-fast", BrewModel: "brew-quality",
				DistillModel: "distill-quality", DrawEffort: "low", BrewEffort: "max",
				DistillEffort: "high", Timeout: "5s",
			},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
	}
	for _, test := range []struct {
		task       Task
		wantModel  string
		wantEffort string
	}{
		{task: TaskDraw, wantModel: "draw-fast", wantEffort: "low"},
		{task: TaskBrew, wantModel: "brew-quality", wantEffort: "max"},
		{task: TaskDistillSelect, wantModel: "distill-quality", wantEffort: "high"},
		{task: TaskDistillGenerate, wantModel: "distill-quality", wantEffort: "high"},
	} {
		if _, err := runner.Run(context.Background(), test.task, "claude-session-t000001", "prompt"); err == nil {
			t.Fatal("expected the capture backend to exit unsuccessfully")
		}
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Fields(string(data))
		if !containsString(args, "--strict-mcp-config") {
			t.Fatalf("%s arguments = %#v, want --strict-mcp-config", test.task, args)
		}
		if containsString(args, "--mcp-config") {
			t.Fatalf("%s arguments unexpectedly contain --mcp-config: %#v", test.task, args)
		}
		if !containsArgumentPair(args, "--model", test.wantModel) {
			t.Fatalf("%s arguments = %#v, want --model %s", test.task, args, test.wantModel)
		}
		if !containsArgumentPair(args, "--effort", test.wantEffort) {
			t.Fatalf("%s arguments = %#v, want --effort %s", test.task, args, test.wantEffort)
		}
		if !containsArgumentPair(args, "--output-format", "json") {
			t.Fatalf("%s arguments = %#v, want --output-format json", test.task, args)
		}
	}
}

func TestCodexCommandRunnerUsesUserConfigAndTaskSpecificModelAndEffort(t *testing.T) {
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "arguments.txt")
	writeExecutable(t, filepath.Join(binDir, "codex"), `#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_PATH"
exit 9
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capturePath)
	root := t.TempDir()
	runner := &CommandRunner{
		Config: config.Config{
			Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
			LLM: config.LLM{
				Backend: "codex-cli", DrawModel: "draw-fast", BrewModel: "brew-quality",
				DistillModel: "distill-quality", DrawEffort: "low", BrewEffort: "high",
				DistillEffort: "max", Timeout: "5s",
			},
		},
		Executable: filepath.Join(root, "knowbrew"),
		WorkDir:    root,
	}
	for _, test := range []struct {
		task       Task
		wantModel  string
		wantEffort string
	}{
		{task: TaskDraw, wantModel: "draw-fast", wantEffort: "low"},
		{task: TaskExtract, wantModel: "brew-quality", wantEffort: "high"},
		{task: TaskBrew, wantModel: "brew-quality", wantEffort: "high"},
		{task: TaskDistillSelect, wantModel: "distill-quality", wantEffort: "max"},
		{task: TaskDistillGenerate, wantModel: "distill-quality", wantEffort: "max"},
	} {
		if _, err := runner.Run(context.Background(), test.task, "codex-session-t000001", "prompt"); err == nil {
			t.Fatal("expected the capture backend to exit unsuccessfully")
		}
		data, err := os.ReadFile(capturePath)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Fields(string(data))
		if containsString(args, "--ignore-user-config") {
			t.Fatalf("%s arguments unexpectedly ignore user config: %#v", test.task, args)
		}
		if !containsString(args, "--json") {
			t.Fatalf("%s arguments = %#v, want --json", test.task, args)
		}
		if !containsArgumentPair(args, "--model", test.wantModel) {
			t.Fatalf("%s arguments = %#v, want --model %s", test.task, args, test.wantModel)
		}
		wantSandbox := "workspace-write"
		if test.task == TaskExtract || test.task == TaskBrew ||
			test.task == TaskDistillSelect || test.task == TaskDistillGenerate {
			wantSandbox = "read-only"
		}
		if !containsArgumentPair(args, "--sandbox", wantSandbox) {
			t.Fatalf("%s arguments = %#v, want --sandbox %s", test.task, args, wantSandbox)
		}
		if len(args) < 3 || args[0] != "exec" || args[1] != "-c" ||
			args[2] != "model_reasoning_effort="+test.wantEffort {
			t.Fatalf(
				"%s arguments = %#v, want exec -c model_reasoning_effort=%s",
				test.task,
				args,
				test.wantEffort,
			)
		}
	}
}

func TestCommandRunnerOmitsEmptyEffortArguments(t *testing.T) {
	binDir := t.TempDir()
	capturePath := filepath.Join(t.TempDir(), "arguments.txt")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$CAPTURE_PATH"
exit 9
`
	for _, executable := range []string{"claude", "codex"} {
		writeExecutable(t, filepath.Join(binDir, executable), script)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CAPTURE_PATH", capturePath)
	for _, backend := range []string{"claude-cli", "codex-cli"} {
		t.Run(backend, func(t *testing.T) {
			root := t.TempDir()
			runner := &CommandRunner{
				Config: config.Config{
					Root: root, Path: filepath.Join(root, ".knowbrew", "config.toml"),
					LLM: config.LLM{Backend: backend, Timeout: "5s"},
				},
				Executable: filepath.Join(root, "knowbrew"),
				WorkDir:    root,
			}
			if _, err := runner.Run(context.Background(), TaskDraw, "feedstock-1", "prompt"); err == nil {
				t.Fatal("expected the capture backend to exit unsuccessfully")
			}
			data, err := os.ReadFile(capturePath)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Fields(string(data))
			if containsString(args, "--effort") {
				t.Fatalf("%s arguments unexpectedly contain --effort: %#v", backend, args)
			}
			for _, argument := range args {
				if strings.HasPrefix(argument, "model_reasoning_effort=") {
					t.Fatalf("%s arguments unexpectedly contain reasoning effort: %#v", backend, args)
				}
			}
		})
	}
}

func containsArgumentPair(arguments []string, name, value string) bool {
	for index := range len(arguments) - 1 {
		if arguments[index] == name && arguments[index+1] == value {
			return true
		}
	}
	return false
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
