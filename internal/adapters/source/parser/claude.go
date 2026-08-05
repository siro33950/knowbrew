package parser

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Claude struct{}

type claudeParserState struct {
	Sequence        int64                   `json:"sequence"`
	OwnerSessionID  string                  `json:"owner_session_id"`
	OwnerIdentified bool                    `json:"owner_identified"`
	Assembler       turnAssemblerCheckpoint `json:"assembler"`
}

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

var ignoredClaudeRecordTypes = map[string]struct{}{
	"attachment": {}, "custom-title": {}, "file-history-snapshot": {},
	"last-prompt": {}, "mode": {}, "permission-mode": {}, "pr-link": {},
	"progress": {}, "queue-operation": {}, "result": {}, "started": {},
	"system": {},
}

var knownClaudeBlockTypes = map[string]struct{}{
	"fallback": {}, "image": {}, "text": {}, "thinking": {},
	"tool_result": {}, "tool_use": {},
}

func (Claude) SessionID(path string) (string, error) {
	fallback := sessionIDFromPath(path)
	found := ""
	err := scanSnapshot(path, func(_ int, raw []byte) (bool, error) {
		var record claudeRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return false, fmt.Errorf("decode Claude record: %w", err)
		}
		if record.SessionID != "" {
			found = record.SessionID
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("scan Claude log %s: %w", path, err)
	}
	if found != "" {
		return found, nil
	}
	return fallback, nil
}

func (Claude) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	return parseClaude(path, false)
}

func (Claude) ParseIncremental(
	path string,
	checkpoint *Checkpoint,
) (IncrementalResult, []diagnostic.Warning, error) {
	position := scanPosition{}
	sequence := int64(0)
	ownerSessionID := sessionIDFromPath(path)
	ownerIdentified := false
	assembler := newTurnAssemblerWithSequence("claude", sessionIDFromPath(path), &sequence)
	if checkpoint != nil {
		position = scanPosition{
			Offset: checkpoint.Offset, Line: checkpoint.Line, SnapshotSize: checkpoint.SnapshotSize,
		}
		var state claudeParserState
		if err := json.Unmarshal(checkpoint.State, &state); err != nil {
			return IncrementalResult{}, nil, fmt.Errorf("restore Claude parser checkpoint: %w", err)
		}
		sequence = state.Sequence
		ownerSessionID = state.OwnerSessionID
		ownerIdentified = state.OwnerIdentified
		assembler = restoreTurnAssembler(state.Assembler, &sequence)
	}
	end, err := scanSnapshotFrom(path, position, MaxJSONLRecordBytes, func(_ int, raw []byte) (bool, error) {
		events, err := decodeClaudeRecord(raw)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if !ownerIdentified && event.Kind == eventUserMessage && event.SessionID != "" {
				ownerSessionID = event.SessionID
				ownerIdentified = true
			}
			if err := assembler.Apply(event); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if err != nil {
		return IncrementalResult{}, nil, fmt.Errorf("parse Claude log %s: %w", path, err)
	}
	assembler.FlushCompleted()
	state, err := json.Marshal(claudeParserState{
		Sequence: sequence, OwnerSessionID: ownerSessionID,
		OwnerIdentified: ownerIdentified, Assembler: assembler.checkpoint(),
	})
	if err != nil {
		return IncrementalResult{}, nil, fmt.Errorf("save Claude parser checkpoint: %w", err)
	}
	candidates := assembler.Drain()
	setSourceOwner(candidates, ownerSessionID)
	return IncrementalResult{
		Candidates: candidates,
		Checkpoint: Checkpoint{
			Offset: end.Offset, Line: end.Line, SnapshotSize: end.SnapshotSize, State: state,
		},
	}, nil, nil
}

func parseClaude(
	path string,
	includeOpen bool,
) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	assembler := newTurnAssembler("claude", sessionIDFromPath(path))
	ownerSessionID := sessionIDFromPath(path)
	ownerIdentified := false
	err := scanSnapshot(path, func(_ int, raw []byte) (bool, error) {
		events, err := decodeClaudeRecord(raw)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if !ownerIdentified && event.Kind == eventUserMessage && event.SessionID != "" {
				ownerSessionID = event.SessionID
				ownerIdentified = true
			}
			if err := assembler.Apply(event); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("parse Claude log %s: %w", path, err)
	}
	candidates := assembler.Finish(includeOpen)
	setSourceOwner(candidates, ownerSessionID)
	return candidates, nil, nil
}

func (Claude) ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error) {
	candidates, _, err := parseClaude(path, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.TurnID == turnID {
			return candidate.Dialogue, nil
		}
	}
	return nil, fmt.Errorf("source turn %s was not found in Claude log %s", turnID, path)
}

func decodeClaudeRecord(raw []byte) ([]sourceEvent, error) {
	var record claudeRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode Claude record: %w", err)
	}
	if record.Type == "" {
		return nil, fmt.Errorf("Claude record has no type")
	}
	switch record.Type {
	case "user":
		return decodeClaudeUser(record, raw)
	case "assistant":
		return decodeClaudeAssistant(record)
	default:
		if _, ignored := ignoredClaudeRecordTypes[record.Type]; ignored {
			return []sourceEvent{{Kind: eventIgnored}}, nil
		}
		return nil, fmt.Errorf("unknown Claude record type %q", record.Type)
	}
}

func decodeClaudeUser(record claudeRecord, raw []byte) ([]sourceEvent, error) {
	if record.Message.Role != "user" {
		return nil, fmt.Errorf("Claude user record has role %q", record.Message.Role)
	}
	if record.IsMeta || record.IsSidechain || record.IsCompactSummary ||
		record.IsVisibleInTranscriptOnly {
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	blocks, err := claudeBlocks(record.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("decode Claude user content: %w", err)
	}
	for _, block := range blocks {
		if _, known := knownClaudeBlockTypes[block.Type]; !known {
			return nil, fmt.Errorf("unknown Claude user content block %q", block.Type)
		}
		if block.Type == "tool_result" {
			return []sourceEvent{{Kind: eventIgnored}}, nil
		}
	}
	text, err := claudeText(record.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("decode Claude user text: %w", err)
	}
	if isClaudeSyntheticQuote(text) {
		return []sourceEvent{{Kind: eventTurnCompleted}}, nil
	}
	if strings.TrimSpace(text) == "" {
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("parse Claude user timestamp: %w", err)
	}
	return []sourceEvent{{
		Kind: eventUserMessage, SessionID: record.SessionID,
		TurnID: sourceTurnID(record.UUID, raw), Timestamp: timestamp,
		CWD: record.CWD, Branch: record.GitBranch, Text: text,
	}}, nil
}

func decodeClaudeAssistant(record claudeRecord) ([]sourceEvent, error) {
	if record.Message.Role != "assistant" {
		return nil, fmt.Errorf("Claude assistant record has role %q", record.Message.Role)
	}
	if record.IsSidechain {
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	blocks, err := claudeBlocks(record.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("decode Claude assistant content: %w", err)
	}
	for _, block := range blocks {
		if _, known := knownClaudeBlockTypes[block.Type]; !known {
			return nil, fmt.Errorf("unknown Claude assistant content block %q", block.Type)
		}
	}
	text, err := claudeText(record.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("decode Claude assistant text: %w", err)
	}
	return []sourceEvent{{
		Kind: eventAssistantMessage, Text: text, Priority: assistantFinal,
		CompletesTurn: record.Message.StopReason == "end_turn",
	}}, nil
}

func isClaudeSyntheticQuote(quote string) bool {
	switch strings.TrimSpace(quote) {
	case "[Request interrupted by user]", "[Request interrupted by user for tool use]":
		return true
	default:
		return false
	}
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
