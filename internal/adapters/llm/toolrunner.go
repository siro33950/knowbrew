package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/invocation/state"
)

type ToolRunner struct {
	Config     config.Config
	Executable string
	WorkDir    string
	Progress   io.Writer
	Verbose    bool
	Client     *http.Client
	progressMu sync.Mutex
}

type chatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []toolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type toolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func NewToolRunner(
	cfg config.Config,
	executable,
	workDir string,
	progress io.Writer,
	verbose ...bool,
) *ToolRunner {
	return &ToolRunner{
		Config: cfg, Executable: executable, WorkDir: workDir, Progress: progress,
		Verbose: len(verbose) > 0 && verbose[0], Client: &http.Client{},
	}
}

func (runner *ToolRunner) Run(
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
	invocationID := newInvocationID()
	defer invocation.Cleanup(runner.Config.Root, invocationID)
	messages := []chatMessage{{Role: "system", Content: toolSystemPrompt(task)}, {Role: "user", Content: prompt}}
	typeNames, err := knowledgeTypeNames(ctx, task)
	if err != nil {
		return RunResult{}, err
	}
	tools := toolSchemas(task, typeNames)
	schema := resultSchema(task, typeNames)
	var usage Usage
	for round := 0; round < 64; round++ {
		message, roundUsage, err := runner.complete(runContext, task, messages, tools, schema)
		usage.Add(roundUsage)
		if err != nil {
			if errors.Is(runContext.Err(), context.DeadlineExceeded) {
				return RunResult{Usage: usage}, fmt.Errorf("%w after %s", ErrTimeout, timeout)
			}
			return RunResult{Usage: usage}, err
		}
		messages = append(messages, message)
		if len(message.ToolCalls) == 0 {
			output := json.RawMessage(strings.TrimSpace(message.Content))
			if !json.Valid(output) {
				return RunResult{Usage: usage}, errors.New("LLM returned invalid structured JSON")
			}
			reads, err := invocation.ReadStateForInvocation(runner.Config.Root, invocationID)
			if err != nil {
				return RunResult{Output: output, Usage: usage}, err
			}
			return RunResult{Output: output, Usage: usage, Reads: applicationReadState(reads)}, nil
		}
		for _, call := range message.ToolCalls {
			if !toolAllowed(task, call.Function.Name) {
				return RunResult{Usage: usage}, fmt.Errorf("tool %q is not allowed for %s", call.Function.Name, task)
			}
			var arguments map[string]any
			if err := decodeToolArguments(call.Function.Arguments, &arguments); err != nil {
				return RunResult{Usage: usage}, fmt.Errorf("decode tool arguments: %w", err)
			}
			commandArgs, err := commandForTool(runner.Executable, call.Function.Name, arguments)
			if err != nil {
				return RunResult{Usage: usage}, err
			}
			command := exec.CommandContext(runContext, commandArgs[0], commandArgs[1:]...)
			configureCommandTermination(command)
			command.Dir = runner.WorkDir
			command.Env = invocationEnvironment(
				os.Environ(), runner.Config.Path, feedstockID, invocationID, task,
			)
			output, commandErr := command.CombinedOutput()
			result := string(output)
			if commandErr != nil {
				result = fmt.Sprintf("error: %v\n%s", commandErr, result)
			}
			if runner.Verbose && runner.Progress != nil && strings.TrimSpace(result) != "" {
				runner.progressMu.Lock()
				_, _ = fmt.Fprintln(runner.Progress, strings.TrimSpace(result))
				runner.progressMu.Unlock()
			}
			if errors.Is(runContext.Err(), context.DeadlineExceeded) {
				diagnostic := compactDiagnostic(result, "")
				if diagnostic == "" {
					return RunResult{Usage: usage}, fmt.Errorf("%w after %s", ErrTimeout, timeout)
				}
				return RunResult{Usage: usage}, fmt.Errorf("%w after %s: %s", ErrTimeout, timeout, diagnostic)
			}
			messages = append(messages, chatMessage{
				Role: "tool", ToolCallID: call.ID, Content: result,
			})
		}
	}
	return RunResult{Usage: usage}, errors.New("LLM exceeded the tool-call retry limit")
}

func (runner *ToolRunner) RunWithUsage(
	ctx context.Context,
	task Task,
	feedstockID,
	prompt string,
) (Usage, error) {
	result, err := runner.Run(ctx, task, feedstockID, prompt)
	return result.Usage, err
}

func decodeToolArguments(raw json.RawMessage, target *map[string]any) error {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return json.Unmarshal([]byte(encoded), target)
	}
	return json.Unmarshal(raw, target)
}

func (runner *ToolRunner) complete(
	ctx context.Context,
	task Task,
	messages []chatMessage,
	tools []map[string]any,
	schema map[string]any,
) (chatMessage, Usage, error) {
	model, err := modelForTask(runner.Config, task)
	if err != nil {
		return chatMessage{}, Usage{}, err
	}
	effort, err := effortForTask(runner.Config, task)
	if err != nil {
		return chatMessage{}, Usage{}, err
	}
	body := map[string]any{"model": model, "messages": messages, "stream": false}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	if runner.Config.LLM.Backend == "api" && strings.TrimSpace(effort) != "" {
		body["reasoning_effort"] = effort
	}
	if runner.Config.LLM.Backend == "ollama" {
		body["format"] = schema
		return runner.completeOllama(ctx, body)
	}
	body["response_format"] = map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name": "knowbrew_result", "strict": false, "schema": schema,
		},
	}
	endpoint := strings.TrimSpace(os.Getenv(config.APIURLEnvironment))
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/chat/completions"
	}
	var response struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
			PromptDetails    struct {
				CachedTokens int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		Error any `json:"error"`
	}
	if err := runner.post(ctx, endpoint, body, &response, os.Getenv(config.APITokenEnvironment)); err != nil {
		return chatMessage{}, Usage{}, err
	}
	usage := usageFromOpenAI(
		response.Usage.PromptTokens,
		response.Usage.CompletionTokens,
		response.Usage.PromptDetails.CachedTokens,
	)
	if len(response.Choices) == 0 {
		return chatMessage{}, usage, errors.New("API returned no choices")
	}
	return response.Choices[0].Message, usage, nil
}

func (runner *ToolRunner) completeOllama(
	ctx context.Context,
	body map[string]any,
) (chatMessage, Usage, error) {
	host := strings.TrimRight(strings.TrimSpace(os.Getenv("OLLAMA_HOST")), "/")
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	var response struct {
		Message         chatMessage `json:"message"`
		Error           string      `json:"error"`
		PromptEvalCount int64       `json:"prompt_eval_count"`
		EvalCount       int64       `json:"eval_count"`
	}
	if err := runner.post(ctx, host+"/api/chat", body, &response, ""); err != nil {
		return chatMessage{}, Usage{}, err
	}
	usage := Usage{InputTokens: response.PromptEvalCount, OutputTokens: response.EvalCount}
	if response.Error != "" {
		return chatMessage{}, usage, errors.New(response.Error)
	}
	return response.Message, usage, nil
}

func (runner *ToolRunner) post(ctx context.Context, endpoint string, body any, target any, token string) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := runner.Client.Do(request)
	if err != nil {
		return fmt.Errorf("call LLM API: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	responseData, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("LLM API returned %s: %s", response.Status, clippedAPI(responseData))
	}
	if err := json.Unmarshal(responseData, target); err != nil {
		return fmt.Errorf("decode LLM API response: %w", err)
	}
	return nil
}

func toolSystemPrompt(task Task) string {
	if task == TaskDraw {
		return "Summarize the target turn supplied in the user prompt and select broad Knowledge type candidates for it. Do not infer from surrounding turns beyond resolving references. If an unresolved reference affects a possible candidate, feedstock_context may be called once. Return only the required structured result."
	}
	if task == TaskExtract {
		return "Extract independently maintainable Knowledge drafts from only the supplied dialogue and context. Return only the required structured result. Never call tools or edit files."
	}
	if task == TaskBrew {
		return "Organize every supplied unorganized Knowledge input against the complete supplied Subject snapshot. Return exactly one action per input. Never call tools or edit files."
	}
	if task == TaskDistillSelect {
		return "Select only supplied invocation-local Knowledge references that can support the supplied document Template. Return only the required structured result. Never call tools or edit files."
	}
	if task == TaskDistillGenerate {
		return "Generate one complete Markdown body from only the supplied Knowledge and return the exact invocation-local references used. Return only the required structured result. Never call tools or edit files."
	}
	return "Return only the required structured result. Never call tools or edit files."
}

func toolSchemas(task Task, typeNames []string) []map[string]any {
	if task == TaskExtract || task == TaskBrew ||
		task == TaskDistillSelect || task == TaskDistillGenerate {
		return nil
	}
	if task == TaskDraw {
		return []map[string]any{
			toolDefinition("feedstock_context", "Load the configured maximum source context for one unresolved target reference", map[string]any{
				"type":     "object",
				"required": []string{"feedstock_id"},
				"properties": map[string]any{
					"feedstock_id": map[string]any{"type": "string"},
				},
			}),
		}
	}
	return nil
}

func toolAllowed(task Task, name string) bool {
	if task == TaskExtract || task == TaskBrew ||
		task == TaskDistillSelect || task == TaskDistillGenerate {
		return false
	}
	if task == TaskDraw {
		return name == "feedstock_context"
	}
	return false
}

func toolDefinition(name, description string, parameters map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": name, "description": description, "parameters": parameters,
		},
	}
}

func clippedAPI(data []byte) string {
	const limit = 1000
	text := strings.TrimSpace(string(data))
	if len(text) > limit {
		end := limit
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		return text[:end] + "…"
	}
	return text
}
