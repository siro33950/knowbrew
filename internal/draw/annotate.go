package draw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/invocation"
	"github.com/siro33950/knowbrew/internal/store"
)

type Annotation struct {
	FeedstockID string
	Assertions  []AssertionInput
}

type AssertionInput struct {
	Type      domain.KnowledgeType `json:"type"`
	Subject   string               `json:"subject"`
	Statement string               `json:"statement"`
	Rationale string               `json:"rationale,omitempty"`
	Trigger   string               `json:"trigger,omitempty"`
}

func Annotate(ctx context.Context, dataStore *store.Store, annotation Annotation) (int, error) {
	if err := invocation.ValidateFeedstock(annotation.FeedstockID); err != nil {
		return 0, err
	}
	feedstock, _, err := dataStore.FindFeedstock(annotation.FeedstockID)
	if err != nil {
		return 0, fmt.Errorf("read feedstock: %w", err)
	}
	if feedstock.AnnotatedAt != nil {
		return 0, fmt.Errorf("feedstock %s is already annotated", annotation.FeedstockID)
	}
	if strings.TrimSpace(feedstock.Summary) == "" {
		return 0, fmt.Errorf("feedstock %s must be summarized before annotation", annotation.FeedstockID)
	}
	now := time.Now().UTC()
	err = dataStore.WithLock(ctx, func() error {
		current, _, err := dataStore.FindFeedstock(annotation.FeedstockID)
		if err != nil {
			return err
		}
		if current.AnnotatedAt != nil {
			return fmt.Errorf("feedstock %s is already annotated", annotation.FeedstockID)
		}
		if strings.TrimSpace(current.Summary) == "" {
			return fmt.Errorf("feedstock %s must be summarized before annotation", annotation.FeedstockID)
		}
		claim, err := invocation.Claim(dataStore.Root)
		if err != nil {
			return err
		}
		succeeded := false
		defer func() {
			if !succeeded {
				invocation.Rollback(claim)
			}
		}()
		subjectEntries, _, err := dataStore.LoadMasters("subjects")
		if err != nil {
			return err
		}
		knownSubjects := make(map[string]struct{}, len(subjectEntries))
		for _, entry := range subjectEntries {
			knownSubjects[entry.Name] = struct{}{}
		}
		assertions, err := buildAssertions(
			dataStore,
			annotation.FeedstockID,
			annotation.Assertions,
			knownSubjects,
		)
		if err != nil {
			return err
		}
		if err := dataStore.AnnotateFeedstock(
			annotation.FeedstockID,
			assertions,
			now,
		); err != nil {
			return err
		}
		succeeded = true
		return nil
	})
	return 0, err
}

func Summarize(ctx context.Context, dataStore *store.Store, feedstockID, summary string) error {
	if err := invocation.ValidateFeedstock(feedstockID); err != nil {
		return err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("summary is required")
	}
	feedstock, _, err := dataStore.FindFeedstock(feedstockID)
	if err != nil {
		return fmt.Errorf("read feedstock: %w", err)
	}
	if feedstock.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already annotated", feedstockID)
	}
	if strings.TrimSpace(feedstock.Summary) != "" {
		return fmt.Errorf("feedstock %s is already summarized", feedstockID)
	}
	return dataStore.WithLock(ctx, func() error {
		current, _, err := dataStore.FindFeedstock(feedstockID)
		if err != nil {
			return err
		}
		if current.AnnotatedAt != nil {
			return fmt.Errorf("feedstock %s is already annotated", feedstockID)
		}
		if strings.TrimSpace(current.Summary) != "" {
			return fmt.Errorf("feedstock %s is already summarized", feedstockID)
		}
		claim, err := invocation.Claim(dataStore.Root)
		if err != nil {
			return err
		}
		succeeded := false
		defer func() {
			if !succeeded {
				invocation.Rollback(claim)
			}
		}()
		if err := dataStore.SummarizeFeedstock(feedstockID, summary); err != nil {
			return err
		}
		succeeded = true
		return nil
	})
}

func buildAssertions(
	dataStore *store.Store,
	feedstockID string,
	inputs []AssertionInput,
	knownSubjects map[string]struct{},
) ([]domain.Assertion, error) {
	if len(inputs) > 32 {
		return nil, errors.New("at most 32 assertions are allowed per feedstock")
	}
	assertions := make([]domain.Assertion, 0, len(inputs))
	seenStatements := make(map[string]struct{}, len(inputs))
	for index, input := range inputs {
		input.Type = domain.KnowledgeType(strings.TrimSpace(string(input.Type)))
		input.Subject = domain.MasterName(input.Subject)
		input.Statement = strings.TrimSpace(input.Statement)
		input.Rationale = strings.TrimSpace(input.Rationale)
		input.Trigger = strings.TrimSpace(input.Trigger)
		if err := dataStore.ValidateKnowledgeType(input.Type); err != nil {
			return nil, fmt.Errorf("assertion %d type: %w", index+1, err)
		}
		if input.Subject != "" {
			if _, exists := knownSubjects[input.Subject]; !exists {
				return nil, fmt.Errorf(
					"assertion %d subject %q is not defined in masters/subjects",
					index+1,
					input.Subject,
				)
			}
		}
		if input.Statement == "" {
			return nil, fmt.Errorf("assertion %d requires statement", index+1)
		}
		if strings.ContainsAny(input.Statement, "\r\n") {
			return nil, fmt.Errorf("assertion %d statement must be one line", index+1)
		}
		if input.Trigger != "" && input.Trigger != "always" {
			return nil, fmt.Errorf("assertion %d has unsupported trigger %q", index+1, input.Trigger)
		}
		statementKey := strings.ToLower(input.Statement) + "\x00" + input.Subject
		if _, exists := seenStatements[statementKey]; exists {
			return nil, fmt.Errorf("assertion %d duplicates another statement", index+1)
		}
		seenStatements[statementKey] = struct{}{}
		assertion := domain.Assertion{
			Type: input.Type, Subject: input.Subject,
			Statement: input.Statement, Rationale: input.Rationale, Trigger: input.Trigger,
		}
		assertion.ID = assertionID(feedstockID, assertion)
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func assertionID(feedstockID string, assertion domain.Assertion) string {
	payload, _ := json.Marshal(struct {
		FeedstockID string               `json:"feedstock_id"`
		Type        domain.KnowledgeType `json:"type"`
		Subject     string               `json:"subject"`
		Statement   string               `json:"statement"`
		Rationale   string               `json:"rationale,omitempty"`
		Trigger     string               `json:"trigger,omitempty"`
	}{
		FeedstockID: feedstockID, Type: assertion.Type, Subject: assertion.Subject,
		Statement: assertion.Statement,
		Rationale: assertion.Rationale, Trigger: assertion.Trigger,
	})
	digest := sha256.Sum256(payload)
	return "as-" + hex.EncodeToString(digest[:16])
}
