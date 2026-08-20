package draw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
)

type Draft struct {
	FeedstockID string
	Summary     string
	Types       []domain.KnowledgeType
}

func ApplyDraft(
	ctx context.Context,
	dataStore Repository,
	guard InvocationGuard,
	draft Draft,
) error {
	if guard == nil {
		guard = unrestrictedInvocation{}
	}
	if err := guard.ValidateFeedstock(draft.FeedstockID); err != nil {
		return err
	}
	summary := strings.TrimSpace(draft.Summary)
	if summary == "" {
		return errors.New("summary is required")
	}
	feedstock, err := dataStore.GetFeedstock(draft.FeedstockID)
	if err != nil {
		return fmt.Errorf("read feedstock: %w", err)
	}
	if feedstock.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already drawn", draft.FeedstockID)
	}
	now := time.Now().UTC()
	return dataStore.WithLock(ctx, func() error {
		current, err := dataStore.GetFeedstock(draft.FeedstockID)
		if err != nil {
			return err
		}
		if current.AnnotatedAt != nil {
			return fmt.Errorf("feedstock %s is already drawn", draft.FeedstockID)
		}
		return guard.Mutate(func() error {
			typeEntries, _, err := dataStore.LoadMasters("types")
			if err != nil {
				return err
			}
			types, err := domain.NormalizeKnowledgeTypes(draft.Types)
			if err != nil {
				return err
			}
			vocabulary := domain.NewVocabulary(typeEntries, nil)
			for _, value := range types {
				if err := vocabulary.ValidateType(value); err != nil {
					return err
				}
			}
			return dataStore.DraftFeedstock(draft.FeedstockID, summary, types, now)
		})
	})
}
