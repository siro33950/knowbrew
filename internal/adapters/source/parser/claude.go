package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Claude struct{}

type claudeRecord struct {
	Type                      string        `json:"type"`
	UUID                      string        `json:"uuid"`
	SessionID                 string        `json:"sessionId"`
	Timestamp                 string        `json:"timestamp"`
	CWD                       string        `json:"cwd"`
	GitBranch                 string        `json:"gitBranch"`
	IsMeta                    bool          `json:"isMeta"`
	IsSidechain               bool          `json:"isSidechain"`
	IsCompactSummary          bool          `json:"isCompactSummary"`
	IsVisibleInTranscriptOnly bool          `json:"isVisibleInTranscriptOnly"`
	Message                   claudeMessage `json:"message"`
}

type claudeMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	StopReason string          `json:"stop_reason"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (Claude) SessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Claude log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	fallback := sessionIDFromPath(path)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var record claudeRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if record.SessionID != "" {
			return record.SessionID, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan Claude log %s: %w", path, err)
	}
	return fallback, nil
}

func (Claude) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open Claude log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	sessionID := sessionIDFromPath(path)
	var candidates []domain.FeedstockCandidate
	var warnings []diagnostic.Warning
	var current *domain.FeedstockCandidate
	var currentComplete bool

	flush := func() {
		if current == nil {
			return
		}
		if currentComplete {
			candidates = append(candidates, *current)
		}
		current = nil
		currentComplete = false
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
		if current != nil && isClaudeSyntheticQuoteRecord(record) {
			currentComplete = true
			continue
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
			if strings.TrimSpace(quote) == "" || isClaudeSyntheticQuote(quote) {
				continue
			}
			if current != nil {
				currentComplete = true
			}
			flush()
			timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
			if err != nil {
				warnings = append(warnings, diagnostic.FromError(
					fmt.Sprintf("%s:%d", path, line),
					fmt.Errorf("parse timestamp: %w", err),
				))
				continue
			}
			turnID := sourceTurnID(record.UUID, scanner.Bytes())
			current = &domain.FeedstockCandidate{
				ID:        FeedstockID("claude", sessionID, turnID),
				TurnID:    turnID,
				Session:   domain.SessionRef{ID: sessionID},
				Timestamp: timestamp,
				Agent:     "claude",
				CWD:       record.CWD,
				Branch:    record.GitBranch,
				Dialogue:  []domain.DialogueMessage{{Role: "user", Content: quote}},
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
			text, decodeErr := claudeText(record.Message.Content)
			if decodeErr == nil && strings.TrimSpace(text) != "" {
				message := domain.DialogueMessage{Role: "assistant", Content: text}
				if len(current.Dialogue) == 1 {
					current.Dialogue = append(current.Dialogue, message)
				} else {
					current.Dialogue[1] = message
				}
			}
			if record.Message.StopReason == "end_turn" {
				currentComplete = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan Claude log %s: %w", path, err)
	}
	flush()
	return candidates, warnings, nil
}

func (Claude) ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Claude log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var userText, finalAssistantText string
	found := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var record claudeRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		if isClaudeHumanMessage(record) {
			quote, decodeErr := claudeText(record.Message.Content)
			if decodeErr == nil && strings.TrimSpace(quote) != "" && !isClaudeSyntheticQuote(quote) {
				recordTurnID := sourceTurnID(record.UUID, scanner.Bytes())
				if found {
					break
				}
				if recordTurnID == turnID {
					found = true
					userText = quote
				}
				continue
			}
		}
		if !found || record.IsSidechain ||
			record.Type != "assistant" || record.Message.Role != "assistant" {
			continue
		}
		text, decodeErr := claudeText(record.Message.Content)
		if decodeErr == nil && strings.TrimSpace(text) != "" {
			finalAssistantText = text
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Claude log %s: %w", path, err)
	}
	if !found {
		return nil, fmt.Errorf("source turn %s was not found in Claude log %s", turnID, path)
	}
	messages := []domain.DialogueMessage{{Role: "user", Content: userText}}
	if strings.TrimSpace(finalAssistantText) != "" {
		messages = append(messages, domain.DialogueMessage{
			Role: "assistant", Content: finalAssistantText,
		})
	}
	return messages, nil
}

func isClaudeHumanMessage(record claudeRecord) bool {
	if record.Type != "user" || record.Message.Role != "user" ||
		record.IsMeta || record.IsSidechain ||
		record.IsCompactSummary || record.IsVisibleInTranscriptOnly {
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

func isClaudeSyntheticQuote(quote string) bool {
	switch strings.TrimSpace(quote) {
	case "[Request interrupted by user]", "[Request interrupted by user for tool use]":
		return true
	default:
		return false
	}
}

func isClaudeSyntheticQuoteRecord(record claudeRecord) bool {
	if record.Type != "user" || record.Message.Role != "user" {
		return false
	}
	quote, err := claudeText(record.Message.Content)
	return err == nil && isClaudeSyntheticQuote(quote)
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
