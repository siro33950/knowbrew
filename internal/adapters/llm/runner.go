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
	"sync"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/invocation/state"
	"github.com/siro33950/knowbrew/internal/application/agent"
)

var ErrTimeout = errors.New("LLM backend timed out")

const (
	TaskSummarize = agent.TaskSummarize
	TaskAnnotate  = agent.TaskAnnotate
	TaskBrew      = agent.TaskBrew

	diagnosticMaxLines = 20
	diagnosticMaxRunes = 2000
)

const diagnosticTruncatedMarker = "[earlier diagnostic output truncated]"

type Task = agent.Task
type Runner = agent.Runner
type RunResult = agent.RunResult

func WithAssertion(ctx context.Context, assertionID string) context.Context {
	return agent.WithAssertion(ctx, assertionID)
}

func assertionFromContext(ctx context.Context) string {
	return agent.AssertionFromContext(ctx)
}

type CommandRunner struct {
	Config     config.Config
	Executable string
	WorkDir    string
	Progress   io.Writer
	Verbose    bool
	progressMu sync.Mutex
}

func New(cfg config.Config, executable, workDir string, progress io.Writer, verbose ...bool) (Runner, error) {
	streamOutput := len(verbose) > 0 && verbose[0]
	if cfg.LLM.Backend == "api" || cfg.LLM.Backend == "ollama" {
		return NewToolRunner(cfg, executable, workDir, progress, streamOutput), nil
	}
	return &CommandRunner{
		Config:     cfg,
		Executable: executable,
		WorkDir:    workDir,
		Progress:   progress,
		Verbose:    streamOutput,
	}, nil
}

func (runner *CommandRunner) Run(
	ctx context.Context,
	task Task,
	feedstockID,
	prompt string,
) (RunResult, error) {
	timeout, err := runner.Config.LLM.TimeoutDuration()
	if err != nil {
		return RunResult{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var command *exec.Cmd
	model, err := modelForTask(runner.Config, task)
	if err != nil {
		return RunResult{}, err
	}
	effort, err := effortForTask(runner.Config, task)
	if err != nil {
		return RunResult{}, err
	}
	typeNames, err := knowledgeTypeNames(ctx, task)
	if err != nil {
		return RunResult{}, err
	}
	schemaData, err := json.Marshal(resultSchema(task, typeNames))
	if err != nil {
		return RunResult{}, fmt.Errorf("encode result schema: %w", err)
	}
	schemaPath, err := writeTemporaryFile("knowbrew-result-schema-", schemaData)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = os.Remove(schemaPath) }()
	resultPath, err := writeTemporaryFile("knowbrew-result-", nil)
	if err != nil {
		return RunResult{}, err
	}
	defer func() { _ = os.Remove(resultPath) }()
	if task != TaskSummarize {
		prompt = fmt.Sprintf("%s\n\nUse this exact knowbrew executable only for the permitted read operations: %s", prompt, runner.Executable)
	}
	switch runner.Config.LLM.Backend {
	case "claude-cli":
		args := []string{
			"-p", prompt,
			"--permission-mode", "dontAsk",
			"--safe-mode",
			"--no-session-persistence",
			"--strict-mcp-config",
			"--output-format", "json",
			"--json-schema", string(schemaData),
		}
		allowed := claudeAllowedTools(runner.Executable, task)
		if len(allowed) == 0 {
			args = append(args, "--tools", "")
		} else {
			args = append(args, "--tools", "Bash", "--allowedTools", strings.Join(allowed, " "))
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		if strings.TrimSpace(effort) != "" {
			args = append(args, "--effort", effort)
		}
		command = exec.CommandContext(runContext, "claude", args...)
	case "codex-cli":
		args := []string{"exec"}
		if strings.TrimSpace(effort) != "" {
			args = append(args, "-c", "model_reasoning_effort="+effort)
		}
		args = append(args,
			"--sandbox", "workspace-write", "--ephemeral",
			"--skip-git-repo-check", "--json", "-C", runner.WorkDir,
			"--output-schema", schemaPath, "--output-last-message", resultPath,
		)
		if model != "" {
			args = append(args, "--model", model)
		}
		args = append(args, prompt)
		command = exec.CommandContext(runContext, "codex", args...)
	default:
		return RunResult{}, fmt.Errorf("unsupported command LLM backend %q", runner.Config.LLM.Backend)
	}
	command.Dir = runner.WorkDir
	invocationID := newInvocationID()
	defer invocation.Cleanup(runner.Config.Root, invocationID)
	command.Env = invocationEnvironment(
		os.Environ(), runner.Config.Path, feedstockID, assertionFromContext(ctx), invocationID,
	)
	stdout := newTailWriter(32 << 10)
	stderr := newTailWriter(32 << 10)
	usageOutput := &usageCapture{}
	if runner.Verbose && runner.Progress != nil {
		liveOutput := lockedWriter{writer: runner.Progress, mu: &runner.progressMu}
		command.Stdout = io.MultiWriter(liveOutput, stdout, usageOutput)
		command.Stderr = io.MultiWriter(liveOutput, stderr)
	} else {
		command.Stdout = io.MultiWriter(stdout, usageOutput)
		command.Stderr = stderr
	}
	if err := command.Run(); err != nil {
		usage := usageOutput.Usage(runner.Config.LLM.Backend)
		diagnostic := commandDiagnostic(stderr.String(), stdout.String(), prompt)
		if errors.Is(runContext.Err(), context.DeadlineExceeded) {
			return RunResult{Usage: usage}, fmt.Errorf("%w after %s%s", ErrTimeout, timeout, diagnostic)
		}
		if errors.Is(runContext.Err(), context.Canceled) {
			return RunResult{Usage: usage}, runContext.Err()
		}
		return RunResult{Usage: usage}, fmt.Errorf("%s failed: %w%s", runner.Config.LLM.Backend, err, diagnostic)
	}
	usage := usageOutput.Usage(runner.Config.LLM.Backend)
	var output json.RawMessage
	if runner.Config.LLM.Backend == "codex-cli" {
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			return RunResult{Usage: usage}, fmt.Errorf("read structured result: %w", readErr)
		}
		output = json.RawMessage(strings.TrimSpace(string(data)))
	} else {
		output, err = claudeStructuredOutput(usageOutput.Bytes())
		if err != nil {
			return RunResult{Usage: usage}, err
		}
	}
	if !json.Valid(output) {
		return RunResult{Usage: usage}, errors.New("backend returned invalid structured JSON")
	}
	reads, err := invocation.ReadStateForInvocation(runner.Config.Root, invocationID)
	if err != nil {
		return RunResult{Output: output, Usage: usage}, err
	}
	return RunResult{Output: output, Usage: usage, Reads: applicationReadState(reads)}, nil
}

func applicationReadState(reads invocation.ReadState) agent.ReadState {
	return agent.ReadState{
		Subject: reads.Subject, Catalog: append([]string(nil), reads.Catalog...),
		CatalogDigest: reads.CatalogDigest, Inspected: append([]string(nil), reads.Inspected...),
		AnnotationContext: reads.AnnotationContext,
	}
}

func (runner *CommandRunner) RunWithUsage(
	ctx context.Context,
	task Task,
	feedstockID,
	prompt string,
) (Usage, error) {
	result, err := runner.Run(ctx, task, feedstockID, prompt)
	return result.Usage, err
}

type lockedWriter struct {
	writer io.Writer
	mu     *sync.Mutex
}

func (writer lockedWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.writer.Write(data)
}

func modelForTask(cfg config.Config, task Task) (string, error) {
	switch task {
	case TaskSummarize, TaskAnnotate:
		return strings.TrimSpace(cfg.LLM.DrawModel), nil
	case TaskBrew:
		return strings.TrimSpace(cfg.LLM.BrewModel), nil
	default:
		return "", fmt.Errorf("unsupported LLM task %q", task)
	}
}

func effortForTask(cfg config.Config, task Task) (string, error) {
	switch task {
	case TaskSummarize, TaskAnnotate:
		return cfg.LLM.DrawEffort, nil
	case TaskBrew:
		return cfg.LLM.BrewEffort, nil
	default:
		return "", fmt.Errorf("unsupported LLM task %q", task)
	}
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

func commandDiagnostic(stderr, stdout, prompt string) string {
	stderr = compactDiagnostic(stderr, prompt)
	stdout = compactDiagnostic(stdout, prompt)
	if stdout != "" && (stdout == stderr || strings.Contains(stderr, stdout)) {
		stdout = ""
	}
	var parts []string
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n" + strings.Join(parts, "\n")
}

func compactDiagnostic(output, prompt string) string {
	if prompt != "" {
		output = strings.ReplaceAll(output, prompt, "")
	}
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	lines := strings.Split(output, "\n")
	runes := []rune(output)
	if len(lines) <= diagnosticMaxLines && len(runes) <= diagnosticMaxRunes {
		return output
	}
	if len(lines) >= diagnosticMaxLines {
		lines = lines[len(lines)-(diagnosticMaxLines-1):]
		output = strings.Join(lines, "\n")
	}
	budget := diagnosticMaxRunes - len([]rune(diagnosticTruncatedMarker)) - 1
	runes = []rune(output)
	if len(runes) > budget {
		output = string(runes[len(runes)-budget:])
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return diagnosticTruncatedMarker
	}
	return diagnosticTruncatedMarker + "\n" + output
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
	if task == TaskSummarize {
		return nil
	}
	if task == TaskAnnotate {
		return []string{pattern("feedstock context *")}
	}
	return []string{
		pattern("knowledge catalog *"),
		pattern("knowledge show *"),
	}
}

func writeTemporaryFile(pattern string, data []byte) (string, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", fmt.Errorf("create temporary result file: %w", err)
	}
	path := file.Name()
	if len(data) > 0 {
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("write temporary result file: %w", err)
		}
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close temporary result file: %w", err)
	}
	return path, nil
}

func claudeStructuredOutput(data []byte) (json.RawMessage, error) {
	var response struct {
		StructuredOutput json.RawMessage `json:"structured_output"`
		Result           string          `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, fmt.Errorf("decode Claude structured response: %w", err)
	}
	if len(response.StructuredOutput) > 0 && string(response.StructuredOutput) != "null" {
		return response.StructuredOutput, nil
	}
	if json.Valid([]byte(response.Result)) {
		return json.RawMessage(response.Result), nil
	}
	return nil, errors.New("claude returned no structured output")
}

func invocationEnvironment(base []string, configPath, feedstockID, assertionID, invocationID string) []string {
	values := map[string]string{
		config.ConfigEnvironment:              configPath,
		config.InvocationFeedstockEnvironment: feedstockID,
		config.InvocationAssertionEnvironment: assertionID,
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
		config.InvocationAssertionEnvironment,
		config.InvocationIDEnvironment,
	} {
		out = append(out, key+"="+values[key])
	}
	return out
}

func commandForTool(executable string, name string, arguments map[string]any) ([]string, error) {
	switch name {
	case "feedstock_context":
		return []string{
			executable, "feedstock", "context", stringValue(arguments, "feedstock_id"),
		}, nil
	case "knowledge_catalog":
		args := []string{"knowledge", "catalog", "--subject", stringValue(arguments, "subject")}
		return append([]string{executable}, args...), nil
	case "knowledge_show":
		args := []string{"knowledge", "show"}
		args = append(args, stringSlice(arguments["knowledge_ids"])...)
		return append([]string{executable}, args...), nil
	default:
		return nil, fmt.Errorf("unsupported tool %q", name)
	}
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
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
