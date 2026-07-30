package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Claude struct{}

type claudeRecord struct {
	Type          string          `json:"type"`
	SessionID     string          `json:"sessionId"`
	Timestamp     string          `json:"timestamp"`
	CWD           string          `json:"cwd"`
	GitBranch     string          `json:"gitBranch"`
	IsMeta        bool            `json:"isMeta"`
	IsSidechain   bool            `json:"isSidechain"`
	Message       claudeMessage   `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

type claudeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (Claude) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open Claude log %s: %w", path, err)
	}
	defer file.Close()

	sessionID := sessionIDFromPath(path)
	var candidates []domain.FeedstockCandidate
	var warnings []diagnostic.Warning
	var current *domain.FeedstockCandidate
	commandByToolID := map[string]int{}
	feedstockNumber := 0

	flush := func() {
		if current == nil {
			return
		}
		current.FilesChanged = domain.UniqueSorted(current.FilesChanged)
		current.Errors = domain.UniqueSorted(current.Errors)
		candidates = append(candidates, *current)
		current = nil
		commandByToolID = map[string]int{}
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var record claudeRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			warnings = append(warnings, diagnostic.FromError(
				fmt.Sprintf("%s:%d", path, line),
				fmt.Errorf("decode Claude record: %w", err),
			))
			continue
		}
		if record.SessionID != "" {
			sessionID = record.SessionID
		}
		if isClaudeHumanMessage(record) {
			quote, err := claudeText(record.Message.Content)
			if err != nil {
				warnings = append(warnings, diagnostic.FromError(
					fmt.Sprintf("%s:%d", path, line),
					fmt.Errorf("decode user content: %w", err),
				))
				continue
			}
			if strings.TrimSpace(quote) == "" {
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
				ID:        FeedstockID("claude", sessionID, feedstockNumber),
				Session:   domain.SessionRef{ID: sessionID, Path: path},
				Timestamp: timestamp,
				Agent:     "claude",
				CWD:       record.CWD,
				Branch:    record.GitBranch,
				UserQuote: quote,
			}
			continue
		}
		if current == nil || record.IsSidechain {
			continue
		}
		if record.CWD != "" {
			current.CWD = record.CWD
		}
		if record.GitBranch != "" {
			current.Branch = record.GitBranch
		}
		if record.Type == "assistant" && record.Message.Role == "assistant" {
			blocks, err := claudeBlocks(record.Message.Content)
			if err != nil {
				warnings = append(warnings, diagnostic.FromError(
					fmt.Sprintf("%s:%d", path, line),
					fmt.Errorf("decode assistant content: %w", err),
				))
				continue
			}
			for _, block := range blocks {
				if block.Type != "tool_use" {
					continue
				}
				switch block.Name {
				case "Bash":
					var input struct {
						Command string `json:"command"`
					}
					if json.Unmarshal(block.Input, &input) == nil && strings.TrimSpace(input.Command) != "" {
						current.Commands = append(current.Commands, domain.Command{Command: input.Command})
						commandByToolID[block.ID] = len(current.Commands) - 1
					}
				case "Write", "Edit", "Read":
					if block.Name == "Read" {
						continue
					}
					var input struct {
						FilePath string `json:"file_path"`
					}
					if json.Unmarshal(block.Input, &input) == nil && input.FilePath != "" {
						current.FilesChanged = append(current.FilesChanged, input.FilePath)
					}
				}
			}
		}
		if record.Type == "user" && record.Message.Role == "user" {
			blocks, err := claudeBlocks(record.Message.Content)
			if err != nil {
				warnings = append(warnings, diagnostic.FromError(
					fmt.Sprintf("%s:%d", path, line),
					fmt.Errorf("decode tool result content: %w", err),
				))
				continue
			}
			for _, block := range blocks {
				if block.Type != "tool_result" {
					continue
				}
				index, ok := commandByToolID[block.ToolUseID]
				if !ok {
					continue
				}
				exitCode, hasExit := claudeExitCode(record.ToolUseResult, block.Content, block.IsError)
				if hasExit {
					current.Commands[index].ExitCode = &exitCode
				}
				if block.IsError || (hasExit && exitCode != 0) {
					current.Errors = append(current.Errors, clipped(rawText(block.Content)))
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan Claude log %s: %w", path, err)
	}
	flush()
	return candidates, warnings, nil
}

func isClaudeHumanMessage(record claudeRecord) bool {
	if record.Type != "user" || record.Message.Role != "user" || record.IsMeta || record.IsSidechain {
		return false
	}
	blocks, err := claudeBlocks(record.Message.Content)
	if err == nil && len(blocks) > 0 {
		for _, block := range blocks {
			if block.Type == "tool_result" {
				return false
			}
		}
	}
	return true
}

func claudeText(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	blocks, err := claudeBlocks(raw)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

func claudeBlocks(raw json.RawMessage) ([]claudeBlock, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var blocks []claudeBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		var text string
		if json.Unmarshal(raw, &text) == nil {
			return nil, nil
		}
		return nil, err
	}
	return blocks, nil
}

var exitCodePattern = regexp.MustCompile(`(?i)exit code\s+([0-9]+)`)

func claudeExitCode(raw, content json.RawMessage, isError bool) (int, bool) {
	if len(raw) > 0 {
		var values map[string]any
		if json.Unmarshal(raw, &values) == nil {
			for _, key := range []string{"exitCode", "exit_code", "code"} {
				switch value := values[key].(type) {
				case float64:
					return int(value), true
				case string:
					if number, err := strconv.Atoi(value); err == nil {
						return number, true
					}
				}
			}
		}
	}
	for _, value := range []string{rawText(raw), rawText(content)} {
		match := exitCodePattern.FindStringSubmatch(value)
		if len(match) == 2 {
			if number, err := strconv.Atoi(match[1]); err == nil {
				return number, true
			}
		}
	}
	if isError {
		return 1, true
	}
	return 0, true
}

func rawText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, block := range blocks {
			if block.Text != "" {
				parts = append(parts, block.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
