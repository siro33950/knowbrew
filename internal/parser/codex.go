package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Codex struct{}

type codexRecord struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type codexPayload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	Message   string          `json:"message"`
	Name      string          `json:"name"`
	CallID    string          `json:"call_id"`
	Arguments json.RawMessage `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Status    string          `json:"status"`
	Stderr    string          `json:"stderr"`
	Changes   json.RawMessage `json:"changes"`
	Git       struct {
		Branch        string `json:"branch"`
		RepositoryURL string `json:"repository_url"`
		RepoURL       string `json:"repo_url"`
	} `json:"git"`
}

func (Codex) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open Codex log %s: %w", path, err)
	}
	defer file.Close()

	sessionID := sessionIDFromPath(path)
	var sessionCWD, branch, repo string
	var candidates []domain.FeedstockCandidate
	var warnings []diagnostic.Warning
	var current *domain.FeedstockCandidate
	commandByCallID := map[string]int{}
	feedstockNumber := 0

	flush := func() {
		if current == nil {
			return
		}
		current.FilesChanged = domain.UniqueSorted(current.FilesChanged)
		current.Errors = domain.UniqueSorted(current.Errors)
		candidates = append(candidates, *current)
		current = nil
		commandByCallID = map[string]int{}
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record codexRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			warnings = append(warnings, diagnostic.FromError(
				fmt.Sprintf("%s:%d", path, line),
				fmt.Errorf("decode Codex record: %w", err),
			))
			continue
		}
		var payload codexPayload
		if err := json.Unmarshal(record.Payload, &payload); err != nil {
			warnings = append(warnings, diagnostic.FromError(
				fmt.Sprintf("%s:%d", path, line),
				fmt.Errorf("decode Codex payload: %w", err),
			))
			continue
		}
		switch record.Type {
		case "session_meta":
			if payload.ID != "" {
				sessionID = payload.ID
			} else if payload.SessionID != "" {
				sessionID = payload.SessionID
			}
			sessionCWD = payload.CWD
			branch = payload.Git.Branch
			repo = payload.Git.RepositoryURL
			if repo == "" {
				repo = payload.Git.RepoURL
			}
		case "turn_context":
			if current != nil && payload.CWD != "" {
				current.CWD = payload.CWD
			}
			if payload.CWD != "" {
				sessionCWD = payload.CWD
			}
		case "event_msg":
			switch payload.Type {
			case "user_message":
				if strings.TrimSpace(payload.Message) == "" {
					continue
				}
				flush()
				feedstockNumber++
				timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
				if err != nil {
					warnings = append(warnings, diagnostic.FromError(
						fmt.Sprintf("%s:%d", path, line),
						fmt.Errorf("parse timestamp: %w", err),
					))
					continue
				}
				current = &domain.FeedstockCandidate{
					ID:        FeedstockID("codex", sessionID, feedstockNumber),
					Session:   domain.SessionRef{ID: sessionID, Path: path},
					Timestamp: timestamp,
					Agent:     "codex",
					CWD:       sessionCWD,
					Repo:      repo,
					Branch:    branch,
					UserQuote: payload.Message,
				}
			case "patch_apply_end":
				if current != nil {
					current.FilesChanged = append(current.FilesChanged, codexChangeFiles(payload.Changes)...)
					if payload.Status != "" && payload.Status != "completed" && payload.Status != "success" {
						current.Errors = append(current.Errors, clipped(payload.Stderr))
					}
				}
			}
		case "response_item":
			if current == nil {
				continue
			}
			switch payload.Type {
			case "function_call", "custom_tool_call":
				argumentText := rawArgumentText(payload.Arguments)
				if argumentText == "" {
					argumentText = rawArgumentText(payload.Input)
				}
				switch payload.Name {
				case "exec_command":
					var input struct {
						Cmd string `json:"cmd"`
					}
					if json.Unmarshal([]byte(argumentText), &input) == nil && input.Cmd != "" {
						current.Commands = append(current.Commands, domain.Command{Command: input.Cmd})
						commandByCallID[payload.CallID] = len(current.Commands) - 1
					}
				case "apply_patch":
					current.FilesChanged = append(current.FilesChanged, patchFiles(argumentText)...)
				}
			case "function_call_output", "custom_tool_call_output":
				index, ok := commandByCallID[payload.CallID]
				if !ok {
					continue
				}
				exitCode, output, hasExit := codexCommandResult(payload.Output)
				if hasExit {
					current.Commands[index].ExitCode = &exitCode
				}
				if hasExit && exitCode != 0 {
					current.Errors = append(current.Errors, clipped(output))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan Codex log %s: %w", path, err)
	}
	flush()
	return candidates, warnings, nil
}

func rawArgumentText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

func codexCommandResult(raw json.RawMessage) (int, string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		var nested struct {
			ExitCode *int   `json:"exit_code"`
			Output   string `json:"output"`
		}
		if json.Unmarshal([]byte(text), &nested) == nil && nested.ExitCode != nil {
			return *nested.ExitCode, nested.Output, true
		}
		return 0, text, false
	}
	var result struct {
		ExitCode *int   `json:"exit_code"`
		Output   string `json:"output"`
	}
	if json.Unmarshal(raw, &result) == nil && result.ExitCode != nil {
		return *result.ExitCode, result.Output, true
	}
	return 0, "", false
}

func codexChangeFiles(raw json.RawMessage) []string {
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return nil
	}
	files := make([]string, 0, len(values))
	for path := range values {
		files = append(files, path)
	}
	return files
}

var patchFilePattern = regexp.MustCompile(`(?m)^\*\*\* (?:Add|Update|Delete) File: (.+)$`)

func patchFiles(patch string) []string {
	var files []string
	for _, match := range patchFilePattern.FindAllStringSubmatch(patch, -1) {
		if len(match) == 2 {
			files = append(files, strings.TrimSpace(match[1]))
		}
	}
	return files
}
