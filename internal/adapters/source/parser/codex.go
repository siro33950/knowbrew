package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Codex struct{}

type codexParserState struct {
	Sequence        int64                              `json:"sequence"`
	ActiveSessionID string                             `json:"active_session_id"`
	OwnerSessionID  string                             `json:"owner_session_id"`
	OwnerIdentified bool                               `json:"owner_identified"`
	MetadataSeen    bool                               `json:"metadata_seen"`
	Excluded        bool                               `json:"excluded"`
	Assemblers      map[string]turnAssemblerCheckpoint `json:"assemblers"`
}

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
	Timestamp string          `json:"timestamp"`
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
}

type codexSessionMetadata struct {
	ID           string          `json:"id"`
	SessionID    string          `json:"session_id"`
	ThreadSource string          `json:"thread_source"`
	Source       json.RawMessage `json:"source"`
}

type codexLegacyHeader struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Git       struct {
		Branch        string `json:"branch"`
		RepositoryURL string `json:"repository_url"`
	} `json:"git"`
}

type codexLegacyItem struct {
	Type    string              `json:"type"`
	ID      string              `json:"id"`
	Role    string              `json:"role"`
	Content []codexContentBlock `json:"content"`
}

type codexContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (Codex) SessionID(path string) (string, error) {
	fallback := sessionIDFromPath(path)
	found := ""
	err := scanSnapshot(path, func(_ int, raw []byte) (bool, error) {
		kind, _, err := classifyCodexRecord(raw)
		if err != nil {
			return false, err
		}
		switch kind {
		case codexCurrent:
			var record codexRecord
			if err := json.Unmarshal(raw, &record); err != nil {
				return false, err
			}
			if record.Type != "session_meta" {
				return true, nil
			}
			var payload codexPayload
			if err := json.Unmarshal(record.Payload, &payload); err != nil {
				return false, err
			}
			found = payload.ID
			if found == "" {
				found = payload.SessionID
			}
		case codexLegacyHeaderRecord:
			var header codexLegacyHeader
			if err := json.Unmarshal(raw, &header); err != nil {
				return false, err
			}
			found = header.ID
		}
		return found == "", nil
	})
	if err != nil {
		return "", fmt.Errorf("scan Codex log %s: %w", path, err)
	}
	if found != "" {
		return found, nil
	}
	return fallback, nil
}

func (Codex) Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	return parseCodex(path, false)
}

func (Codex) ParseIncremental(
	path string,
	checkpoint *Checkpoint,
) (IncrementalResult, []diagnostic.Warning, error) {
	position := scanPosition{}
	sequence := int64(0)
	activeSessionID := sessionIDFromPath(path)
	ownerSessionID := activeSessionID
	ownerIdentified := false
	metadataSeen := false
	excluded := false
	assemblers := make(map[string]*turnAssembler)
	if checkpoint != nil {
		position = scanPosition{
			Offset: checkpoint.Offset, Line: checkpoint.Line, SnapshotSize: checkpoint.SnapshotSize,
		}
		var state codexParserState
		if err := json.Unmarshal(checkpoint.State, &state); err != nil {
			return IncrementalResult{}, nil, fmt.Errorf("restore Codex parser checkpoint: %w", err)
		}
		sequence = state.Sequence
		activeSessionID = state.ActiveSessionID
		ownerSessionID = state.OwnerSessionID
		ownerIdentified = state.OwnerIdentified
		metadataSeen = state.MetadataSeen
		excluded = state.Excluded
		for sessionID, saved := range state.Assemblers {
			assemblers[sessionID] = restoreTurnAssembler(saved, &sequence)
		}
	}
	assemblerFor := func(sessionID string) *turnAssembler {
		if sessionID == "" {
			sessionID = sessionIDFromPath(path)
		}
		assembler, exists := assemblers[sessionID]
		if !exists {
			assembler = newTurnAssemblerWithSequence("codex", sessionID, &sequence)
			assemblers[sessionID] = assembler
		}
		return assembler
	}
	end := position
	var err error
	collector := newWarningCollector(path)
	if !excluded {
		end, err = scanSnapshotFrom(path, position, MaxJSONLRecordBytes, func(_ int, raw []byte) (bool, error) {
			if !metadataSeen {
				metadata, found, err := currentCodexSessionMetadata(raw)
				if err != nil {
					return false, err
				}
				if found {
					metadataSeen = true
					if codexSessionIsSubagent(metadata) {
						excluded = true
						return false, nil
					}
				}
			}
			events, err := decodeCodexRecord(raw, collector.add)
			if err != nil {
				return false, err
			}
			for _, event := range events {
				if event.Kind == eventSessionStarted && strings.TrimSpace(event.SessionID) != "" {
					if !ownerIdentified {
						ownerSessionID = event.SessionID
						ownerIdentified = true
					}
					activeSessionID = event.SessionID
				} else if event.Kind == eventUserMessage && strings.TrimSpace(event.SessionID) != "" {
					activeSessionID = event.SessionID
				}
				if err := assemblerFor(activeSessionID).Apply(event); err != nil {
					return false, err
				}
			}
			return true, nil
		})
	} else if info, statErr := os.Stat(path); statErr != nil {
		err = statErr
	} else {
		end.Offset = info.Size()
		end.SnapshotSize = info.Size()
	}
	if err != nil {
		return IncrementalResult{}, collector.warnings, fmt.Errorf("parse Codex log %s: %w", path, err)
	}
	var candidates []domain.FeedstockCandidate
	savedAssemblers := make(map[string]turnAssemblerCheckpoint, len(assemblers))
	for sessionID, assembler := range assemblers {
		assembler.FlushCompleted()
		candidates = append(candidates, assembler.Drain()...)
		savedAssemblers[sessionID] = assembler.checkpoint()
	}
	slices.SortStableFunc(candidates, func(left, right domain.FeedstockCandidate) int {
		return int(left.SourceSequence - right.SourceSequence)
	})
	setSourceOwner(candidates, ownerSessionID)
	state, err := json.Marshal(codexParserState{
		Sequence: sequence, ActiveSessionID: activeSessionID,
		OwnerSessionID: ownerSessionID, OwnerIdentified: ownerIdentified,
		MetadataSeen: metadataSeen, Excluded: excluded, Assemblers: savedAssemblers,
	})
	if err != nil {
		return IncrementalResult{}, collector.warnings, fmt.Errorf("save Codex parser checkpoint: %w", err)
	}
	return IncrementalResult{
		Candidates: candidates, Excluded: excluded,
		Checkpoint: Checkpoint{
			Offset: end.Offset, Line: end.Line, SnapshotSize: end.SnapshotSize, State: state,
		},
	}, collector.warnings, nil
}

func parseCodex(
	path string,
	includeOpen bool,
) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	sequence := int64(0)
	assemblers := make(map[string]*turnAssembler)
	activeSessionID := sessionIDFromPath(path)
	ownerSessionID := activeSessionID
	ownerIdentified := false
	metadataSeen := false
	excluded := false
	assemblerFor := func(sessionID string) *turnAssembler {
		if sessionID == "" {
			sessionID = sessionIDFromPath(path)
		}
		assembler, exists := assemblers[sessionID]
		if !exists {
			assembler = newTurnAssemblerWithSequence("codex", sessionID, &sequence)
			assemblers[sessionID] = assembler
		}
		return assembler
	}
	collector := newWarningCollector(path)
	err := scanSnapshot(path, func(_ int, raw []byte) (bool, error) {
		if !metadataSeen {
			metadata, found, err := currentCodexSessionMetadata(raw)
			if err != nil {
				return false, err
			}
			if found {
				metadataSeen = true
				if codexSessionIsSubagent(metadata) {
					excluded = true
					return false, nil
				}
			}
		}
		events, err := decodeCodexRecord(raw, collector.add)
		if err != nil {
			return false, err
		}
		for _, event := range events {
			if event.Kind == eventSessionStarted && strings.TrimSpace(event.SessionID) != "" {
				if !ownerIdentified {
					ownerSessionID = event.SessionID
					ownerIdentified = true
				}
				activeSessionID = event.SessionID
			} else if event.Kind == eventUserMessage && strings.TrimSpace(event.SessionID) != "" {
				activeSessionID = event.SessionID
			}
			if err := assemblerFor(activeSessionID).Apply(event); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	if err != nil {
		return nil, collector.warnings, fmt.Errorf("parse Codex log %s: %w", path, err)
	}
	if excluded {
		return nil, collector.warnings, nil
	}
	var candidates []domain.FeedstockCandidate
	for _, assembler := range assemblers {
		candidates = append(candidates, assembler.Finish(includeOpen)...)
	}
	slices.SortStableFunc(candidates, func(left, right domain.FeedstockCandidate) int {
		return int(left.SourceSequence - right.SourceSequence)
	})
	setSourceOwner(candidates, ownerSessionID)
	return candidates, collector.warnings, nil
}

func currentCodexSessionMetadata(raw []byte) (codexSessionMetadata, bool, error) {
	kind, _, err := classifyCodexRecord(raw)
	if err != nil {
		return codexSessionMetadata{}, false, err
	}
	if kind != codexCurrent {
		return codexSessionMetadata{}, false, nil
	}
	var record codexRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return codexSessionMetadata{}, false, err
	}
	if record.Type != "session_meta" {
		return codexSessionMetadata{}, false, nil
	}
	var metadata codexSessionMetadata
	if err := json.Unmarshal(record.Payload, &metadata); err != nil {
		return codexSessionMetadata{}, false, err
	}
	return metadata, true, nil
}

func codexSessionIsSubagent(metadata codexSessionMetadata) bool {
	if strings.EqualFold(strings.TrimSpace(metadata.ThreadSource), "subagent") {
		return true
	}
	var sourceName string
	if json.Unmarshal(metadata.Source, &sourceName) == nil {
		return strings.EqualFold(strings.TrimSpace(sourceName), "subagent")
	}
	var source struct {
		Subagent json.RawMessage `json:"subagent"`
	}
	if json.Unmarshal(metadata.Source, &source) != nil {
		return false
	}
	trimmed := strings.TrimSpace(string(source.Subagent))
	return trimmed != "" && trimmed != "null"
}

func (Codex) ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error) {
	candidates, _, err := parseCodex(path, true)
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate.TurnID == turnID {
			return candidate.Dialogue, nil
		}
	}
	return nil, fmt.Errorf("source turn %s was not found in Codex log %s", turnID, path)
}

type codexRecordKind uint8

const (
	codexCurrent codexRecordKind = iota + 1
	codexLegacyHeaderRecord
	codexLegacyStateRecord
	codexLegacyItemRecord
)

func classifyCodexRecord(raw []byte) (codexRecordKind, string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return 0, "", fmt.Errorf("decode Codex record: %w", err)
	}
	if fields == nil {
		return 0, "Codex record is not an object", nil
	}
	_, hasType := fields["type"]
	_, hasTimestamp := fields["timestamp"]
	_, hasPayload := fields["payload"]
	_, hasRecordType := fields["record_type"]
	_, hasID := fields["id"]
	_, hasInstructions := fields["instructions"]
	_, hasGit := fields["git"]
	matches := make([]codexRecordKind, 0, 2)
	if hasType && hasTimestamp && hasPayload {
		matches = append(matches, codexCurrent)
	}
	if !hasType && hasID && hasTimestamp && (hasInstructions || hasGit) {
		matches = append(matches, codexLegacyHeaderRecord)
	}
	if hasRecordType {
		matches = append(matches, codexLegacyStateRecord)
	}
	if hasType && !hasTimestamp && !hasPayload {
		matches = append(matches, codexLegacyItemRecord)
	}
	if len(matches) == 0 {
		return 0, "unknown Codex record shape", nil
	}
	if len(matches) > 1 {
		return 0, "ambiguous Codex record shape", nil
	}
	return matches[0], "", nil
}

func decodeCodexRecord(raw []byte, warn func(reason string)) ([]sourceEvent, error) {
	kind, shapeIssue, err := classifyCodexRecord(raw)
	if err != nil {
		return nil, err
	}
	if shapeIssue != "" {
		warn(shapeIssue)
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	switch kind {
	case codexCurrent:
		return decodeCurrentCodexRecord(raw, warn)
	case codexLegacyHeaderRecord:
		var header codexLegacyHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, err
		}
		timestamp, err := time.Parse(time.RFC3339Nano, header.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse legacy Codex session timestamp: %w", err)
		}
		return []sourceEvent{{
			Kind: eventSessionStarted, SessionID: header.ID, Timestamp: timestamp,
			Repo: header.Git.RepositoryURL, Branch: header.Git.Branch,
		}}, nil
	case codexLegacyStateRecord:
		var state struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, err
		}
		return []sourceEvent{{Kind: eventIgnored}}, nil
	case codexLegacyItemRecord:
		return decodeLegacyCodexItem(raw, warn)
	default:
		return nil, fmt.Errorf("unsupported Codex record shape")
	}
}

func decodeCurrentCodexRecord(raw []byte, warn func(reason string)) ([]sourceEvent, error) {
	var record codexRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("decode current Codex record: %w", err)
	}
	switch record.Type {
	case "session_meta", "turn_context", "event_msg", "response_item":
	default:
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	var payload codexPayload
	if len(record.Payload) == 0 || string(record.Payload) == "null" {
		return nil, fmt.Errorf("Codex %s record has no payload", record.Type)
	}
	if err := json.Unmarshal(record.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode Codex %s payload: %w", record.Type, err)
	}
	switch record.Type {
	case "session_meta":
		timestampText := payload.Timestamp
		if timestampText == "" {
			timestampText = record.Timestamp
		}
		timestamp, err := time.Parse(time.RFC3339Nano, timestampText)
		if err != nil {
			return nil, fmt.Errorf("parse Codex session timestamp: %w", err)
		}
		sessionID := payload.ID
		if sessionID == "" {
			sessionID = payload.SessionID
		}
		repo := payload.Git.RepositoryURL
		if repo == "" {
			repo = payload.Git.RepoURL
		}
		return []sourceEvent{{
			Kind: eventSessionStarted, SessionID: sessionID, Timestamp: timestamp,
			CWD: payload.CWD, Repo: repo, Branch: payload.Git.Branch,
		}}, nil
	case "turn_context":
		return []sourceEvent{{
			Kind: eventTurnStarted, TurnID: payload.TurnID, CWD: payload.CWD,
		}}, nil
	case "event_msg":
		return decodeCodexEvent(record, payload, raw, warn)
	case "response_item":
		return decodeCodexResponseItem(payload, warn)
	default:
		return nil, fmt.Errorf("unsupported Codex record type %q", record.Type)
	}
}

func decodeCodexEvent(
	record codexRecord,
	payload codexPayload,
	raw []byte,
	warn func(reason string),
) ([]sourceEvent, error) {
	switch payload.Type {
	case "user_message":
		if strings.TrimSpace(payload.Message) == "" {
			return []sourceEvent{{Kind: eventIgnored}}, nil
		}
		timestamp, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			return nil, fmt.Errorf("parse Codex user timestamp: %w", err)
		}
		return []sourceEvent{{
			Kind: eventUserMessage, TurnID: payload.TurnID,
			FallbackTurnID: sourceTurnID("", raw), Timestamp: timestamp,
			Text: payload.Message,
		}}, nil
	case "agent_message":
		return []sourceEvent{{
			Kind: eventAssistantMessage, Text: payload.Message, Priority: assistantEvent,
		}}, nil
	case "task_complete", "turn_aborted":
		return []sourceEvent{{Kind: eventTurnCompleted, TurnID: payload.TurnID}}, nil
	default:
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
}

func decodeCodexResponseItem(payload codexPayload, warn func(reason string)) ([]sourceEvent, error) {
	if payload.Type != "message" {
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	if payload.Role != "assistant" {
		if payload.Role != "user" && payload.Role != "developer" && payload.Role != "system" {
			warn(fmt.Sprintf("unknown Codex response message role %q", payload.Role))
		}
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	text, err := codexText(payload.Content, "output_text", warn)
	if err != nil {
		return nil, err
	}
	switch payload.Phase {
	case "final_answer":
		return []sourceEvent{{
			Kind: eventAssistantMessage, Text: text, Priority: assistantFinal,
			CompletesTurn: true,
		}}, nil
	case "":
		return []sourceEvent{{
			Kind: eventAssistantMessage, Text: text, Priority: assistantFallback,
		}}, nil
	case "commentary":
		return []sourceEvent{{Kind: eventIgnored}}, nil
	default:
		warn(fmt.Sprintf("unknown Codex assistant message phase %q", payload.Phase))
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
}

func decodeLegacyCodexItem(raw []byte, warn func(reason string)) ([]sourceEvent, error) {
	var item codexLegacyItem
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, fmt.Errorf("decode legacy Codex item: %w", err)
	}
	if item.Type != "message" {
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
	switch item.Role {
	case "user":
		text := textFromCodexBlocks(item.Content, "input_text", warn)
		if strings.TrimSpace(text) == "" {
			return []sourceEvent{{Kind: eventIgnored}}, nil
		}
		turnID := ""
		if strings.TrimSpace(item.ID) != "" {
			turnID = "record-" + item.ID
		}
		return []sourceEvent{{
			Kind: eventUserMessage, TurnID: turnID,
			FallbackTurnID: sourceTurnID("", raw), Text: text,
		}}, nil
	case "assistant":
		text := textFromCodexBlocks(item.Content, "output_text", warn)
		return []sourceEvent{{
			Kind: eventAssistantMessage, Text: text, Priority: assistantFinal,
			CompletesTurn: true,
		}}, nil
	case "developer", "system":
		return []sourceEvent{{Kind: eventIgnored}}, nil
	default:
		warn(fmt.Sprintf("unknown legacy Codex message role %q", item.Role))
		return []sourceEvent{{Kind: eventIgnored}}, nil
	}
}

func codexText(raw json.RawMessage, expected string, warn func(reason string)) (string, error) {
	var blocks []codexContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return "", fmt.Errorf("decode Codex message content: %w", err)
	}
	return textFromCodexBlocks(blocks, expected, warn), nil
}

func textFromCodexBlocks(blocks []codexContentBlock, expected string, warn func(reason string)) string {
	values := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type != expected {
			warn(fmt.Sprintf("unknown Codex message content block %q", block.Type))
			continue
		}
		if strings.TrimSpace(block.Text) != "" {
			values = append(values, block.Text)
		}
	}
	return strings.Join(values, "\n")
}
