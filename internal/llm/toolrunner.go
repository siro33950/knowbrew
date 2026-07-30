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
	"unicode/utf8"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/invocation"
)

type ToolRunner struct {
	Config     config.Config
	Executable string
	WorkDir    string
	Progress   io.Writer
	Client     *http.Client
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

func NewToolRunner(cfg config.Config, executable, workDir string, progress io.Writer) *ToolRunner {
	return &ToolRunner{
		Config: cfg, Executable: executable, WorkDir: workDir, Progress: progress,
		Client: &http.Client{},
	}
}

func (runner *ToolRunner) Run(ctx context.Context, task Task, feedstockID, prompt string) error {
	timeout, err := runner.Config.LLM.TimeoutDuration()
	if err != nil {
		return err
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	invocationID := newInvocationID()
	defer invocation.Cleanup(runner.Config.Root, invocationID)
	messages := []chatMessage{{Role: "system", Content: toolSystemPrompt(task)}, {Role: "user", Content: prompt}}
	tools := toolSchemas(task)
	for round := 0; round < 12; round++ {
		message, err := runner.complete(runContext, messages, tools)
		if err != nil {
			if errors.Is(runContext.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w after %s", ErrTimeout, timeout)
			}
			return err
		}
		messages = append(messages, message)
		if len(message.ToolCalls) == 0 {
			if task == TaskBrew {
				return nil
			}
			return errors.New("LLM returned no annotation command")
		}
		mutations := 0
		for _, call := range message.ToolCalls {
			if isMutationTool(call.Function.Name) {
				mutations++
			}
		}
		if task == TaskAnnotate && len(message.ToolCalls) != 1 {
			return errors.New("LLM must issue exactly one feedstock annotation")
		}
		if mutations > 1 {
			return errors.New("LLM must issue at most one knowledge mutation")
		}
		for _, call := range message.ToolCalls {
			if !toolAllowed(task, call.Function.Name) {
				return fmt.Errorf("tool %q is not allowed for %s", call.Function.Name, task)
			}
			var arguments map[string]any
			if err := decodeToolArguments(call.Function.Arguments, &arguments); err != nil {
				return fmt.Errorf("decode tool arguments: %w", err)
			}
			commandArgs, err := commandForTool(runner.Executable, call.Function.Name, arguments)
			if err != nil {
				return err
			}
			command := exec.CommandContext(runContext, commandArgs[0], commandArgs[1:]...)
			command.Dir = runner.WorkDir
			command.Env = invocationEnvironment(os.Environ(), runner.Config.Path, feedstockID, invocationID)
			output, commandErr := command.CombinedOutput()
			result := string(output)
			if commandErr != nil {
				result = fmt.Sprintf("error: %v\n%s", commandErr, result)
			}
			if runner.Progress != nil && strings.TrimSpace(result) != "" {
				fmt.Fprintln(runner.Progress, strings.TrimSpace(result))
			}
			if commandErr == nil && isMutationTool(call.Function.Name) {
				return nil
			}
			if errors.Is(runContext.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%w after %s: %s", ErrTimeout, timeout, strings.TrimSpace(result))
			}
			messages = append(messages, chatMessage{
				Role: "tool", ToolCallID: call.ID, Content: result,
			})
		}
	}
	return errors.New("LLM exceeded the tool-call retry limit")
}

func decodeToolArguments(raw json.RawMessage, target *map[string]any) error {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return json.Unmarshal([]byte(encoded), target)
	}
	return json.Unmarshal(raw, target)
}

func (runner *ToolRunner) complete(ctx context.Context, messages []chatMessage, tools []map[string]any) (chatMessage, error) {
	body := map[string]any{
		"model":    runner.Config.LLM.Model,
		"messages": messages,
		"tools":    tools,
		"stream":   false,
	}
	if runner.Config.LLM.Backend == "ollama" {
		return runner.completeOllama(ctx, body)
	}
	endpoint := strings.TrimSpace(os.Getenv(config.APIURLEnvironment))
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/chat/completions"
	}
	var response struct {
		Choices []struct {
			Message chatMessage `json:"message"`
		} `json:"choices"`
		Error any `json:"error"`
	}
	if err := runner.post(ctx, endpoint, body, &response, os.Getenv(config.APITokenEnvironment)); err != nil {
		return chatMessage{}, err
	}
	if len(response.Choices) == 0 {
		return chatMessage{}, errors.New("API returned no choices")
	}
	return response.Choices[0].Message, nil
}

func (runner *ToolRunner) completeOllama(ctx context.Context, body map[string]any) (chatMessage, error) {
	host := strings.TrimRight(strings.TrimSpace(os.Getenv("OLLAMA_HOST")), "/")
	if host == "" {
		host = "http://127.0.0.1:11434"
	}
	var response struct {
		Message chatMessage `json:"message"`
		Error   string      `json:"error"`
	}
	if err := runner.post(ctx, host+"/api/chat", body, &response, ""); err != nil {
		return chatMessage{}, err
	}
	if response.Error != "" {
		return chatMessage{}, errors.New(response.Error)
	}
	return response.Message, nil
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
	defer response.Body.Close()
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
	if task == TaskAnnotate {
		return "Classify the supplied user feedstock. Use feedstock_annotate. Do not return the classification as prose."
	}
	return "Use the read tools to inspect context, then choose exactly one knowledge mutation or make no mutation for NOOP. Never edit files directly."
}

func toolSchemas(task Task) []map[string]any {
	if task == TaskAnnotate {
		return []map[string]any{toolDefinition("feedstock_annotate", "Finalize one feedstock classification", map[string]any{
			"type":     "object",
			"required": []string{"feedstock_id", "summary", "speech_acts", "topics", "subjects"},
			"properties": map[string]any{
				"feedstock_id": map[string]any{"type": "string"},
				"summary":      map[string]any{"type": "string"},
				"speech_acts":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"topics":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"subjects":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"new_topics":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "name=one-line definition"},
				"new_subjects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "name=one-line definition"},
			},
		})}
	}
	return []map[string]any{
		toolDefinition("knowledge_search", "Search active or pending knowledge", knowledgeSearchSchema()),
		toolDefinition("feedstock_search", "Search immutable feedstock summaries", feedstockSearchSchema()),
		toolDefinition("show", "Read selected feedstock originals", showSchema()),
		toolDefinition("knowledge_create", "Create one pending knowledge record", knowledgeCreateSchema()),
		toolDefinition("knowledge_add_source", "Add evidence to an existing knowledge", sourceSchema()),
		toolDefinition("knowledge_invalidate", "Invalidate a contradicted knowledge", sourceSchema()),
	}
}

func toolAllowed(task Task, name string) bool {
	if task == TaskAnnotate {
		return name == "feedstock_annotate"
	}
	switch name {
	case "knowledge_search", "feedstock_search", "show",
		"knowledge_create", "knowledge_add_source", "knowledge_invalidate":
		return true
	default:
		return false
	}
}

func isMutationTool(name string) bool {
	switch name {
	case "feedstock_annotate", "knowledge_create", "knowledge_add_source", "knowledge_invalidate":
		return true
	default:
		return false
	}
}

func toolDefinition(name, description string, parameters map[string]any) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name": name, "description": description, "parameters": parameters,
		},
	}
}

func knowledgeCreateSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"slug", "applies_when", "body", "sources", "topics"},
		"properties": map[string]any{
			"slug":         map[string]any{"type": "string"},
			"applies_when": map[string]any{"type": "string"},
			"body":         map[string]any{"type": "string"},
			"sources":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"topics":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"project":      map[string]any{"type": "string"},
			"trigger":      map[string]any{"type": "string"},
			"new_topics": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
				"description": "Unknown topics as name=one-line definition",
			},
			"new_subjects": map[string]any{
				"type": "array", "maxItems": 1, "items": map[string]any{"type": "string"},
				"description": "The unknown project subject as name=one-line definition",
			},
		},
	}
}

func commonSearchProperties() map[string]any {
	return map[string]any{
		"keywords":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"subject":    map[string]any{"type": "string"},
		"topic":      map[string]any{"type": "string"},
		"since":      map[string]any{"type": "string"},
		"until":      map[string]any{"type": "string"},
		"limit":      map[string]any{"type": "integer", "minimum": 1},
		"max_tokens": map[string]any{"type": "integer", "minimum": 1},
	}
}

func knowledgeSearchSchema() map[string]any {
	properties := commonSearchProperties()
	properties["include_pending"] = map[string]any{"type": "boolean"}
	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

func feedstockSearchSchema() map[string]any {
	properties := commonSearchProperties()
	properties["session"] = map[string]any{"type": "string"}
	properties["agent"] = map[string]any{"type": "string"}
	properties["last"] = map[string]any{"type": "integer", "minimum": 1}
	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

func showSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"feedstock_ids"},
		"properties": map[string]any{
			"feedstock_ids": map[string]any{
				"type": "array", "minItems": 1, "items": map[string]any{"type": "string"},
			},
		},
	}
}

func sourceSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"slug", "sources"},
		"properties": map[string]any{
			"slug":    map[string]any{"type": "string"},
			"sources": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
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
