package parser

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
)

type sourceEventKind uint8

const (
	eventIgnored sourceEventKind = iota
	eventSessionStarted
	eventTurnStarted
	eventUserMessage
	eventAssistantMessage
	eventTurnCompleted
)

type assistantPriority uint8

const (
	assistantEvent assistantPriority = iota + 1
	assistantFallback
	assistantFinal
)

type sourceEvent struct {
	Kind           sourceEventKind
	SessionID      string
	TurnID         string
	FallbackTurnID string
	Timestamp      time.Time
	CWD            string
	Repo           string
	Branch         string
	Text           string
	Priority       assistantPriority
	CompletesTurn  bool
}

type turnAssembler struct {
	agent            string
	sessionID        string
	sessionTimestamp time.Time
	cwd              string
	repo             string
	branch           string
	pendingTurnID    string
	current          *domain.FeedstockCandidate
	currentSourceID  string
	currentComplete  bool
	assistantText    string
	assistantRank    assistantPriority
	sequence         *int64
	usedTurnIDs      map[string]struct{}
	candidates       []domain.FeedstockCandidate
}

type turnAssemblerCheckpoint struct {
	Agent            string               `json:"agent"`
	SessionID        string               `json:"session_id"`
	SessionTimestamp time.Time            `json:"session_timestamp"`
	CWD              string               `json:"cwd"`
	Repo             string               `json:"repo"`
	Branch           string               `json:"branch"`
	PendingTurnID    string               `json:"pending_turn_id"`
	Current          *candidateCheckpoint `json:"current,omitempty"`
	CurrentSourceID  string               `json:"current_source_turn_id,omitempty"`
	CurrentComplete  bool                 `json:"current_complete"`
	AssistantText    string               `json:"assistant_text"`
	AssistantRank    assistantPriority    `json:"assistant_rank"`
	UsedTurnIDs      []string             `json:"used_turn_ids,omitempty"`
}

type candidateCheckpoint struct {
	ID                   string                   `json:"id"`
	TurnID               string                   `json:"turn_id"`
	Session              domain.SessionRef        `json:"session"`
	Timestamp            time.Time                `json:"timestamp"`
	Agent                string                   `json:"agent"`
	CWD                  string                   `json:"cwd,omitempty"`
	Repo                 string                   `json:"repo,omitempty"`
	Branch               string                   `json:"branch,omitempty"`
	Dialogue             []domain.DialogueMessage `json:"dialogue"`
	SourceSequence       int64                    `json:"source_sequence"`
	SourceOwnerSessionID string                   `json:"source_owner_session_id"`
}

func newTurnAssembler(agent, fallbackSessionID string) *turnAssembler {
	sequence := int64(0)
	return newTurnAssemblerWithSequence(agent, fallbackSessionID, &sequence)
}

func newTurnAssemblerWithSequence(
	agent,
	fallbackSessionID string,
	sequence *int64,
) *turnAssembler {
	return &turnAssembler{agent: agent, sessionID: fallbackSessionID, sequence: sequence}
}

func restoreTurnAssembler(
	checkpoint turnAssemblerCheckpoint,
	sequence *int64,
) *turnAssembler {
	assembler := &turnAssembler{
		agent: checkpoint.Agent, sessionID: checkpoint.SessionID,
		sessionTimestamp: checkpoint.SessionTimestamp,
		cwd:              checkpoint.CWD, repo: checkpoint.Repo, branch: checkpoint.Branch,
		pendingTurnID:   checkpoint.PendingTurnID,
		currentSourceID: checkpoint.CurrentSourceID,
		currentComplete: checkpoint.CurrentComplete,
		assistantText:   checkpoint.AssistantText, assistantRank: checkpoint.AssistantRank,
		sequence: sequence,
	}
	if len(checkpoint.UsedTurnIDs) > 0 {
		assembler.usedTurnIDs = make(map[string]struct{}, len(checkpoint.UsedTurnIDs))
		for _, turnID := range checkpoint.UsedTurnIDs {
			assembler.usedTurnIDs[turnID] = struct{}{}
		}
	}
	if checkpoint.Current != nil {
		current := checkpoint.Current.candidate()
		assembler.current = &current
	}
	return assembler
}

func (assembler *turnAssembler) checkpoint() turnAssemblerCheckpoint {
	checkpoint := turnAssemblerCheckpoint{
		Agent: assembler.agent, SessionID: assembler.sessionID,
		SessionTimestamp: assembler.sessionTimestamp,
		CWD:              assembler.cwd, Repo: assembler.repo, Branch: assembler.branch,
		PendingTurnID:   assembler.pendingTurnID,
		CurrentSourceID: assembler.currentSourceID,
		CurrentComplete: assembler.currentComplete,
		AssistantText:   assembler.assistantText, AssistantRank: assembler.assistantRank,
	}
	if len(assembler.usedTurnIDs) > 0 {
		checkpoint.UsedTurnIDs = make([]string, 0, len(assembler.usedTurnIDs))
		for turnID := range assembler.usedTurnIDs {
			checkpoint.UsedTurnIDs = append(checkpoint.UsedTurnIDs, turnID)
		}
		slices.Sort(checkpoint.UsedTurnIDs)
	}
	if assembler.current != nil {
		current := checkpointCandidate(*assembler.current)
		checkpoint.Current = &current
	}
	return checkpoint
}

func checkpointCandidate(candidate domain.FeedstockCandidate) candidateCheckpoint {
	return candidateCheckpoint{
		ID: candidate.ID, TurnID: candidate.TurnID, Session: candidate.Session,
		Timestamp: candidate.Timestamp, Agent: candidate.Agent,
		CWD: candidate.CWD, Repo: candidate.Repo, Branch: candidate.Branch,
		Dialogue:             append([]domain.DialogueMessage(nil), candidate.Dialogue...),
		SourceSequence:       candidate.SourceSequence,
		SourceOwnerSessionID: candidate.SourceOwnerSessionID,
	}
}

func (checkpoint candidateCheckpoint) candidate() domain.FeedstockCandidate {
	return domain.FeedstockCandidate{
		ID: checkpoint.ID, TurnID: checkpoint.TurnID, Session: checkpoint.Session,
		Timestamp: checkpoint.Timestamp, Agent: checkpoint.Agent,
		CWD: checkpoint.CWD, Repo: checkpoint.Repo, Branch: checkpoint.Branch,
		Dialogue:             append([]domain.DialogueMessage(nil), checkpoint.Dialogue...),
		SourceSequence:       checkpoint.SourceSequence,
		SourceOwnerSessionID: checkpoint.SourceOwnerSessionID,
	}
}

func (assembler *turnAssembler) Apply(event sourceEvent) error {
	switch event.Kind {
	case eventIgnored:
		return nil
	case eventSessionStarted:
		if event.SessionID != "" {
			if assembler.current != nil && assembler.sessionID != "" &&
				assembler.sessionID != event.SessionID {
				return fmt.Errorf(
					"session ID changed from %q to %q within an open turn",
					assembler.sessionID, event.SessionID,
				)
			}
			assembler.sessionID = event.SessionID
		}
		if !event.Timestamp.IsZero() {
			assembler.sessionTimestamp = event.Timestamp
		}
		assembler.updateEnvironment(event)
	case eventTurnStarted:
		if strings.TrimSpace(event.TurnID) == "" {
			return fmt.Errorf("turn context has no turn ID")
		}
		assembler.updateEnvironment(event)
		if assembler.current != nil && !assembler.currentComplete &&
			assembler.currentSourceID == event.TurnID {
			assembler.current.CWD = assembler.cwd
			assembler.current.Repo = assembler.repo
			assembler.current.Branch = assembler.branch
			return nil
		}
		if assembler.current != nil {
			assembler.currentComplete = true
			assembler.flush()
		}
		assembler.pendingTurnID = event.TurnID
	case eventUserMessage:
		if event.SessionID != "" {
			assembler.sessionID = event.SessionID
		}
		assembler.updateEnvironment(event)
		if assembler.current != nil {
			assembler.currentComplete = true
			assembler.flush()
		}
		turnID := strings.TrimSpace(event.TurnID)
		if turnID == "" {
			turnID = strings.TrimSpace(assembler.pendingTurnID)
		}
		if turnID == "" {
			turnID = strings.TrimSpace(event.FallbackTurnID)
		}
		assembler.pendingTurnID = ""
		if turnID == "" {
			return fmt.Errorf("user message has no source turn identity")
		}
		sourceTurnID := turnID
		turnID = assembler.claimTurnID(turnID)
		timestamp := event.Timestamp
		if timestamp.IsZero() {
			timestamp = assembler.sessionTimestamp
		}
		if timestamp.IsZero() {
			return fmt.Errorf("user message has no timestamp")
		}
		(*assembler.sequence)++
		assembler.currentSourceID = sourceTurnID
		assembler.current = &domain.FeedstockCandidate{
			ID:             FeedstockID(assembler.agent, assembler.sessionID, turnID),
			TurnID:         turnID,
			Session:        domain.SessionRef{ID: assembler.sessionID},
			Timestamp:      timestamp,
			Agent:          assembler.agent,
			CWD:            assembler.cwd,
			Repo:           assembler.repo,
			Branch:         assembler.branch,
			SourceSequence: *assembler.sequence,
			Dialogue: []domain.DialogueMessage{{
				Role: "user", Content: event.Text,
			}},
		}
	case eventAssistantMessage:
		if assembler.current == nil {
			return nil
		}
		if strings.TrimSpace(event.Text) != "" && event.Priority >= assembler.assistantRank {
			assembler.assistantText = event.Text
			assembler.assistantRank = event.Priority
		}
		if event.CompletesTurn {
			assembler.currentComplete = true
		}
	case eventTurnCompleted:
		if assembler.current != nil &&
			(event.TurnID == "" || event.TurnID == assembler.currentSourceID) {
			assembler.currentComplete = true
		}
	default:
		return fmt.Errorf("unsupported normalized source event %d", event.Kind)
	}
	return nil
}

func (assembler *turnAssembler) claimTurnID(turnID string) string {
	if assembler.usedTurnIDs == nil {
		assembler.usedTurnIDs = map[string]struct{}{}
	}
	unique := turnID
	for occurrence := 2; ; occurrence++ {
		if _, taken := assembler.usedTurnIDs[unique]; !taken {
			break
		}
		unique = fmt.Sprintf("%s.%d", turnID, occurrence)
	}
	assembler.usedTurnIDs[unique] = struct{}{}
	return unique
}

func (assembler *turnAssembler) Finish(includeOpen bool) []domain.FeedstockCandidate {
	if includeOpen && assembler.current != nil {
		assembler.currentComplete = true
	}
	assembler.flush()
	return assembler.candidates
}

func (assembler *turnAssembler) FlushCompleted() {
	if assembler.current != nil && assembler.currentComplete {
		assembler.flush()
	}
}

func (assembler *turnAssembler) Drain() []domain.FeedstockCandidate {
	candidates := assembler.candidates
	assembler.candidates = nil
	return candidates
}

func (assembler *turnAssembler) updateEnvironment(event sourceEvent) {
	if event.CWD != "" {
		assembler.cwd = event.CWD
	}
	if event.Repo != "" {
		assembler.repo = event.Repo
	}
	if event.Branch != "" {
		assembler.branch = event.Branch
	}
}

func (assembler *turnAssembler) flush() {
	if assembler.current == nil {
		return
	}
	if strings.TrimSpace(assembler.assistantText) != "" {
		assembler.current.Dialogue = append(assembler.current.Dialogue, domain.DialogueMessage{
			Role: "assistant", Content: assembler.assistantText,
		})
	}
	if assembler.currentComplete {
		assembler.candidates = append(assembler.candidates, *assembler.current)
	}
	assembler.current = nil
	assembler.currentSourceID = ""
	assembler.currentComplete = false
	assembler.assistantText = ""
	assembler.assistantRank = 0
}
