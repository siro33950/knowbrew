package llm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/invocation"
)

type Task string

var ErrTimeout = errors.New("LLM backend timed out")

const (
	TaskAnnotate Task = "annotate"
	TaskBrew     Task = "brew"
)

type Runner interface {
	Run(context.Context, Task, string, string) error
}

type CommandRunner struct {
	Config     config.Config
	Executable string
	WorkDir    string
	Progress   io.Writer
}

func New(cfg config.Config, executable, workDir string, progress io.Writer) (Runner, error) {
	if cfg.LLM.Backend == "api" || cfg.LLM.Backend == "ollama" {
		return NewToolRunner(cfg, executable, workDir, progress), nil
	}
	return &CommandRunner{
		Config:     cfg,
		Executable: executable,
		WorkDir:    workDir,
		Progress:   progress,
	}, nil
}

func (runner *CommandRunner) Run(ctx context.Context, task Task, feedstockID, prompt string) error {
	timeout, err := runner.Config.LLM.TimeoutDuration()
	if err != nil {
		return err
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var command *exec.Cmd
	prompt = fmt.Sprintf("%s\n\nUse this exact knowbrew executable for every operation: %s", prompt, runner.Executable)
	switch runner.Config.LLM.Backend {
	case "claude-cli":
		args := []string{
			"-p", prompt,
			"--tools", "Bash",
			"--allowedTools", strings.Join(claudeAllowedTools(runner.Executable, task), " "),
			"--permission-mode", "dontAsk",
			"--safe-mode",
			"--no-session-persistence",
		}
		if runner.Config.LLM.Model != "" {
			args = append(args, "--model", runner.Config.LLM.Model)
		}
		command = exec.CommandContext(runContext, "claude", args...)
	case "codex-cli":
		args := []string{
			"exec", "--sandbox", "workspace-write", "--ephemeral",
			"--ignore-user-config", "--skip-git-repo-check", "-C", runner.WorkDir,
		}
		if runner.Config.LLM.Model != "" {
			args = append(args, "--model", runner.Config.LLM.Model)
		}
		args = append(args, prompt)
		command = exec.CommandContext(runContext, "codex", args...)
	default:
		return fmt.Errorf("unsupported command LLM backend %q", runner.Config.LLM.Backend)
	}
	command.Dir = runner.WorkDir
	invocationID := newInvocationID()
	defer invocation.Cleanup(runner.Config.Root, invocationID)
	command.Env = invocationEnvironment(os.Environ(), runner.Config.Path, feedstockID, invocationID)
	stdout := newTailWriter(32 << 10)
	stderr := newTailWriter(32 << 10)
	if runner.Progress != nil {
		command.Stdout = io.MultiWriter(runner.Progress, stdout)
		command.Stderr = io.MultiWriter(runner.Progress, stderr)
	} else {
		command.Stdout = stdout
		command.Stderr = stderr
	}
	if err := command.Run(); err != nil {
		if invocation.Completed(runner.Config.Root, invocationID) {
			return nil
		}
		diagnostic := commandDiagnostic(stderr.String(), stdout.String())
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%w after %s%s", ErrTimeout, timeout, diagnostic)
		}
		if errors.Is(runContext.Err(), context.Canceled) {
			return runContext.Err()
		}
		return fmt.Errorf("%s failed: %w%s", runner.Config.LLM.Backend, err, diagnostic)
	}
	return nil
}

type tailWriter struct {
	data  []byte
	limit int
}

func newTailWriter(limit int) *tailWriter {
	return &tailWriter{limit: limit}
}

func (writer *tailWriter) Write(data []byte) (int, error) {
	length := len(data)
	if writer.limit <= 0 {
		return length, nil
	}
	writer.data = append(writer.data, data...)
	if len(writer.data) > writer.limit {
		writer.data = append([]byte(nil), writer.data[len(writer.data)-writer.limit:]...)
	}
	return length, nil
}

func (writer *tailWriter) String() string {
	return strings.ToValidUTF8(string(writer.data), "�")
}

func commandDiagnostic(stderr, stdout string) string {
	var parts []string
	if value := strings.TrimSpace(stderr); value != "" {
		parts = append(parts, "stderr:\n"+value)
	}
	if value := strings.TrimSpace(stdout); value != "" {
		parts = append(parts, "stdout:\n"+value)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}

func newInvocationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}

func claudeAllowedTools(executable string, task Task) []string {
	pattern := func(command string) string {
		return "Bash(" + executable + " " + command + ")"
	}
	if task == TaskAnnotate {
		return []string{pattern("feedstock annotate *")}
	}
	return []string{
		pattern("knowledge create *"),
		pattern("knowledge add-source *"),
		pattern("knowledge invalidate *"),
		pattern("knowledge"),
		pattern("knowledge -- *"),
		pattern("knowledge --include-pending *"),
		pattern("knowledge --subject *"),
		pattern("knowledge --topic *"),
		pattern("knowledge --since *"),
		pattern("knowledge --until *"),
		pattern("knowledge --limit *"),
		pattern("knowledge --max-tokens *"),
		pattern("feedstock"),
		pattern("feedstock -- *"),
		pattern("feedstock --subject *"),
		pattern("feedstock --topic *"),
		pattern("feedstock --since *"),
		pattern("feedstock --until *"),
		pattern("feedstock --limit *"),
		pattern("feedstock --max-tokens *"),
		pattern("feedstock --session *"),
		pattern("feedstock --agent *"),
		pattern("feedstock --last *"),
		pattern("show *"),
	}
}

func invocationEnvironment(base []string, configPath, feedstockID, invocationID string) []string {
	values := map[string]string{
		config.ConfigEnvironment:              configPath,
		config.InvocationFeedstockEnvironment: feedstockID,
		config.InvocationIDEnvironment:        invocationID,
	}
	out := make([]string, 0, len(base)+len(values))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			out = append(out, entry)
		}
	}
	for _, key := range []string{
		config.ConfigEnvironment,
		config.InvocationFeedstockEnvironment,
		config.InvocationIDEnvironment,
	} {
		out = append(out, key+"="+values[key])
	}
	return out
}

func commandForTool(executable string, name string, arguments map[string]any) ([]string, error) {
	switch name {
	case "feedstock_annotate":
		args := []string{"feedstock", "annotate", stringValue(arguments, "feedstock_id")}
		args = appendRepeated(args, "--speech-act", stringSlice(arguments["speech_acts"]))
		args = appendRepeated(args, "--topic", stringSlice(arguments["topics"]))
		args = appendRepeated(args, "--subject", stringSlice(arguments["subjects"]))
		args = append(args, "--summary", stringValue(arguments, "summary"))
		args = appendRepeated(args, "--new-topic", stringSlice(arguments["new_topics"]))
		args = appendRepeated(args, "--new-subject", stringSlice(arguments["new_subjects"]))
		return append([]string{executable}, args...), nil
	case "knowledge_create":
		args := []string{"knowledge", "create", stringValue(arguments, "slug"),
			"--applies-when", stringValue(arguments, "applies_when"),
			"--body", stringValue(arguments, "body"),
		}
		args = appendRepeated(args, "--source", stringSlice(arguments["sources"]))
		args = appendRepeated(args, "--topic", stringSlice(arguments["topics"]))
		if value := stringValue(arguments, "project"); value != "" {
			args = append(args, "--project", value)
		}
		if value := stringValue(arguments, "trigger"); value != "" {
			args = append(args, "--trigger", value)
		}
		args = appendRepeated(args, "--new-topic", stringSlice(arguments["new_topics"]))
		args = appendRepeated(args, "--new-subject", stringSlice(arguments["new_subjects"]))
		return append([]string{executable}, args...), nil
	case "knowledge_add_source":
		args := []string{"knowledge", "add-source", stringValue(arguments, "slug")}
		args = appendRepeated(args, "--source", stringSlice(arguments["sources"]))
		return append([]string{executable}, args...), nil
	case "knowledge_invalidate":
		args := []string{"knowledge", "invalidate", stringValue(arguments, "slug")}
		args = appendRepeated(args, "--source", stringSlice(arguments["sources"]))
		return append([]string{executable}, args...), nil
	case "knowledge_search":
		args := []string{"knowledge"}
		if boolValue(arguments, "include_pending") {
			args = append(args, "--include-pending")
		}
		args = appendSearchFlags(args, arguments)
		args = appendKeywords(args, arguments)
		return append([]string{executable}, args...), nil
	case "feedstock_search":
		args := []string{"feedstock"}
		args = appendSearchFlags(args, arguments)
		args = appendStringFlag(args, "--session", stringValue(arguments, "session"))
		args = appendStringFlag(args, "--agent", stringValue(arguments, "agent"))
		args = appendIntFlag(args, "--last", intValue(arguments, "last"))
		args = appendKeywords(args, arguments)
		return append([]string{executable}, args...), nil
	case "show":
		args := append([]string{"show"}, stringSlice(arguments["feedstock_ids"])...)
		return append([]string{executable}, args...), nil
	default:
		return nil, fmt.Errorf("unsupported tool %q", name)
	}
}

func appendSearchFlags(args []string, arguments map[string]any) []string {
	args = appendStringFlag(args, "--subject", stringValue(arguments, "subject"))
	args = appendStringFlag(args, "--topic", stringValue(arguments, "topic"))
	args = appendStringFlag(args, "--since", stringValue(arguments, "since"))
	args = appendStringFlag(args, "--until", stringValue(arguments, "until"))
	args = appendIntFlag(args, "--limit", intValue(arguments, "limit"))
	args = appendIntFlag(args, "--max-tokens", intValue(arguments, "max_tokens"))
	return args
}

func appendStringFlag(args []string, flag, value string) []string {
	if strings.TrimSpace(value) != "" {
		args = append(args, flag, value)
	}
	return args
}

func appendIntFlag(args []string, flag string, value int) []string {
	if value > 0 {
		args = append(args, flag, fmt.Sprintf("%d", value))
	}
	return args
}

func appendKeywords(args []string, arguments map[string]any) []string {
	keywords := stringSlice(arguments["keywords"])
	if len(keywords) > 0 {
		args = append(args, "--")
		args = append(args, keywords...)
	}
	return args
}

func appendRepeated(args []string, flag string, values []string) []string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			args = append(args, flag, value)
		}
	}
	return args
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}

func boolValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func intValue(values map[string]any, key string) int {
	switch value := values[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	default:
		return 0
	}
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}
