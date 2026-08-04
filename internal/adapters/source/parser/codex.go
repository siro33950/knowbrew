package parser

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
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
	TurnID    string          `json:"turn_id"`
	SessionID string          `json:"session_id"`
	CWD       string          `json:"cwd"`
	Message   string          `json:"message"`
	Role      string          `json:"role"`
	Phase     string          `json:"phase"`
	Content   json.RawMessage `json:"content"`
	Git       struct {
		Branch        string `json:"branch"`
		RepositoryURL string `json:"repository_url"`
		RepoURL       string `json:"repo_url"`
	} `json:"git"`
	InternalChatMessageMetadata struct {
		TurnID string `json:"turn_id"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (Codex) SessionID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open Codex log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	fallback := sessionIDFromPath(path)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	for scanner.Scan() {
		var record codexRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record.Type != "session_meta" {
			continue
		}
		var payload codexPayload
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		if payload.ID != "" {
			return payload.ID, nil
		}
		if payload.SessionID != "" {
			return payload.SessionID, nil
		}
		return fallback, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan Codex log %s: %w", path, err)
	}
	return fallback, nil
}

func (Codex) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open Codex log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	sessionID := sessionIDFromPath(path)
	var sessionCWD, branch, repo string
	var nextTurnID string
	var candidates []domain.FeedstockCandidate
	var warnings []diagnostic.Warning
	var current *domain.FeedstockCandidate
	var currentComplete bool
	var finalAssistantText, fallbackAssistantText, eventAssistantText string
	var sawPhasedAssistant bool

	flush := func() {
		if current == nil {
			return
		}
		assistantText := finalAssistantText
		if !sawPhasedAssistant && assistantText == "" {
			assistantText = fallbackAssistantText
			if assistantText == "" {
				assistantText = eventAssistantText
			}
		}
		if strings.TrimSpace(assistantText) != "" {
			current.Dialogue = append(current.Dialogue, domain.DialogueMessage{
				Role: "assistant", Content: assistantText,
			})
		}
		if currentComplete {
			candidates = append(candidates, *current)
		}
		current = nil
		currentComplete = false
		finalAssistantText = ""
		fallbackAssistantText = ""
		eventAssistantText = ""
		sawPhasedAssistant = false
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
			if current != nil && payload.TurnID == current.TurnID {
				if payload.CWD != "" {
					sessionCWD = payload.CWD
					current.CWD = payload.CWD
				}
				continue
			}
			if current != nil {
				currentComplete = true
			}
			flush()
			nextTurnID = payload.TurnID
			if payload.CWD != "" {
				sessionCWD = payload.CWD
			}
		case "event_msg":
			switch payload.Type {
			case "user_message":
				if strings.TrimSpace(payload.Message) == "" {
					nextTurnID = ""
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
					nextTurnID = ""
					continue
				}
				turnID := sourceTurnID(nextTurnID, scanner.Bytes())
				nextTurnID = ""
				current = &domain.FeedstockCandidate{
					ID:        FeedstockID("codex", sessionID, turnID),
					TurnID:    turnID,
					Session:   domain.SessionRef{ID: sessionID},
					Timestamp: timestamp,
					Agent:     "codex",
					CWD:       sessionCWD,
					Repo:      repo,
					Branch:    branch,
					Dialogue:  []domain.DialogueMessage{{Role: "user", Content: payload.Message}},
				}
			case "agent_message":
				if current != nil && strings.TrimSpace(payload.Message) != "" {
					eventAssistantText = payload.Message
				}
			case "task_complete", "turn_aborted":
				if current != nil && payload.TurnID == current.TurnID {
					currentComplete = true
				}
			}
		case "response_item":
			if current == nil || payload.Type != "message" || payload.Role != "assistant" {
				continue
			}
			text := codexOutputText(payload.Content)
			if strings.TrimSpace(text) == "" {
				if payload.Phase != "" {
					sawPhasedAssistant = true
				}
				continue
			}
			switch payload.Phase {
			case "final_answer":
				sawPhasedAssistant = true
				finalAssistantText = text
				currentComplete = true
			case "":
				fallbackAssistantText = text
			default:
				sawPhasedAssistant = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, warnings, fmt.Errorf("scan Codex log %s: %w", path, err)
	}
	flush()
	return candidates, warnings, nil
}

func (Codex) ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Codex log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	var userText, finalAssistantText, fallbackAssistantText, eventAssistantText string
	var nextTurnID string
	found := false
	sawPhasedAssistant := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
scan:
	for scanner.Scan() {
		var record codexRecord
		if json.Unmarshal(scanner.Bytes(), &record) != nil {
			continue
		}
		var payload codexPayload
		if json.Unmarshal(record.Payload, &payload) != nil {
			continue
		}
		switch record.Type {
		case "turn_context":
			if found {
				if payload.TurnID == turnID {
					continue
				}
				break scan
			}
			nextTurnID = payload.TurnID
		case "event_msg":
			switch payload.Type {
			case "user_message":
				recordTurnID := sourceTurnID(nextTurnID, scanner.Bytes())
				nextTurnID = ""
				if found {
					break scan
				}
				if recordTurnID == turnID && strings.TrimSpace(payload.Message) != "" {
					found = true
					userText = payload.Message
				}
			case "agent_message":
				if found && strings.TrimSpace(payload.Message) != "" {
					eventAssistantText = payload.Message
				}
			}
		case "response_item":
			if !found || payload.Type != "message" || payload.Role != "assistant" {
				continue
			}
			text := codexOutputText(payload.Content)
			if strings.TrimSpace(text) == "" {
				if payload.Phase != "" {
					sawPhasedAssistant = true
				}
				continue
			}
			switch payload.Phase {
			case "final_answer":
				sawPhasedAssistant = true
				finalAssistantText = text
			case "":
				fallbackAssistantText = text
			default:
				sawPhasedAssistant = true
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan Codex log %s: %w", path, err)
	}
	if !found {
		return nil, fmt.Errorf("source turn %s was not found in Codex log %s", turnID, path)
	}
	if !sawPhasedAssistant && finalAssistantText == "" {
		finalAssistantText = fallbackAssistantText
		if finalAssistantText == "" {
			finalAssistantText = eventAssistantText
		}
	}
	messages := []domain.DialogueMessage{{Role: "user", Content: userText}}
	if strings.TrimSpace(finalAssistantText) != "" {
		messages = append(messages, domain.DialogueMessage{
			Role: "assistant", Content: finalAssistantText,
		})
	}
	return messages, nil
}

func codexOutputText(raw json.RawMessage) string {
	var blocks []codexContentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var values []string
	for _, block := range blocks {
		if block.Type == "output_text" && block.Text != "" {
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "\n")
}
