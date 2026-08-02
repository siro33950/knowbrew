package draw

import (
	"errors"
	"fmt"
	"strings"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type AnnotationTurn struct {
	Offset        int    `json:"offset"`
	UserInput     string `json:"user_input"`
	AgentResponse string `json:"agent_response,omitempty"`
}

type AnnotationContext struct {
	FeedstockID     string           `json:"feedstock_id"`
	TargetUserInput string           `json:"target_user_input"`
	PriorTurns      []AnnotationTurn `json:"prior_turns"`
}

type SummaryMaterial struct {
	FeedstockID   string `json:"feedstock_id"`
	UserInput     string `json:"user_input"`
	AgentResponse string `json:"agent_response,omitempty"`
}

func LoadSummaryMaterial(
	sources SourceGateway,
	dataStore Repository,
	feedstockID string,
) (SummaryMaterial, []diagnostic.Warning, error) {
	feedstock, err := dataStore.GetFeedstock(feedstockID)
	if err != nil {
		return SummaryMaterial{}, nil, err
	}
	candidates, warnings, err := sources.ParseSession(feedstock.Agent, feedstock.Session.Path)
	if err != nil {
		return SummaryMaterial{}, warnings, err
	}
	material, err := summaryMaterialFromCandidates(candidates, feedstockID)
	return material, warnings, err
}

func LoadAnnotationContext(
	sources SourceGateway,
	dataStore Repository,
	feedstockID string,
	count int,
) (AnnotationContext, []diagnostic.Warning, error) {
	feedstock, err := dataStore.GetFeedstock(feedstockID)
	if err != nil {
		return AnnotationContext{}, nil, err
	}
	candidates, warnings, err := sources.ParseSession(feedstock.Agent, feedstock.Session.Path)
	if err != nil {
		return AnnotationContext{}, warnings, err
	}
	context, err := annotationContextFromCandidates(candidates, feedstockID, count)
	return context, warnings, err
}

func annotationContextFromCandidates(
	candidates []domain.FeedstockCandidate,
	feedstockID string,
	count int,
) (AnnotationContext, error) {
	if count < 0 {
		return AnnotationContext{}, errors.New("context turn count must be at least 0")
	}
	session, targetIndex, err := sourceSession(candidates, feedstockID)
	if err != nil {
		return AnnotationContext{}, err
	}
	start := max(0, targetIndex-count)
	priorTurns := make([]AnnotationTurn, 0, targetIndex-start)
	for index := start; index < targetIndex; index++ {
		priorTurns = append(priorTurns, structuredPriorTurn(
			session[index].Dialogue,
			index-targetIndex,
		))
	}
	target := summaryMaterialFromCandidate(session[targetIndex])
	return AnnotationContext{
		FeedstockID:     feedstockID,
		TargetUserInput: target.UserInput,
		PriorTurns:      priorTurns,
	}, nil
}

func summaryMaterialFromCandidates(
	candidates []domain.FeedstockCandidate,
	feedstockID string,
) (SummaryMaterial, error) {
	session, targetIndex, err := sourceSession(candidates, feedstockID)
	if err != nil {
		return SummaryMaterial{}, err
	}
	return summaryMaterialFromCandidate(session[targetIndex]), nil
}

func sourceSession(
	candidates []domain.FeedstockCandidate,
	feedstockID string,
) ([]domain.FeedstockCandidate, int, error) {
	targetIndex := -1
	for index := range candidates {
		if candidates[index].ID == feedstockID {
			targetIndex = index
			break
		}
	}
	if targetIndex < 0 {
		return nil, -1, fmt.Errorf("feedstock %s was not found in its source log", feedstockID)
	}
	target := candidates[targetIndex]
	session := make([]domain.FeedstockCandidate, 0, len(candidates))
	filteredTargetIndex := -1
	for _, candidate := range candidates {
		if candidate.Agent != target.Agent || candidate.Session.ID != target.Session.ID {
			continue
		}
		if candidate.ID == feedstockID {
			filteredTargetIndex = len(session)
		}
		session = append(session, candidate)
	}
	if filteredTargetIndex < 0 {
		return nil, -1, fmt.Errorf("feedstock %s is missing from its source session", feedstockID)
	}
	return session, filteredTargetIndex, nil
}

func summaryMaterialFromCandidate(candidate domain.FeedstockCandidate) SummaryMaterial {
	var userInputs, agentResponses []string
	for _, message := range candidate.Dialogue {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case "user":
			userInputs = append(userInputs, content)
		case "assistant":
			agentResponses = append(agentResponses, limitAssistantResponse(content))
		}
	}
	return SummaryMaterial{
		FeedstockID:   candidate.ID,
		UserInput:     strings.Join(userInputs, "\n\n"),
		AgentResponse: strings.Join(agentResponses, "\n\n"),
	}
}

func structuredPriorTurn(messages []domain.DialogueMessage, offset int) AnnotationTurn {
	var userInputs, agentResponses []string
	for _, message := range messages {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		switch message.Role {
		case "user":
			userInputs = append(userInputs, content)
		case "assistant":
			agentResponses = append(agentResponses, limitBothEnds(
				content,
				annotationContextAssistantLimitBytes,
				annotationContextAssistantTruncatedMarker,
			))
		}
	}
	return AnnotationTurn{
		Offset:        offset,
		UserInput:     strings.Join(userInputs, "\n\n"),
		AgentResponse: strings.Join(agentResponses, "\n\n"),
	}
}
