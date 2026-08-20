package draw

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/domain"
)

type Annotation struct {
	FeedstockID string
	Types       []domain.KnowledgeType
}

func Annotate(
	ctx context.Context,
	dataStore Repository,
	guard InvocationGuard,
	annotation Annotation,
) (int, error) {
	if guard == nil {
		guard = unrestrictedInvocation{}
	}
	if err := guard.ValidateFeedstock(annotation.FeedstockID); err != nil {
		return 0, err
	}
	feedstock, err := dataStore.GetFeedstock(annotation.FeedstockID)
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
		current, err := dataStore.GetFeedstock(annotation.FeedstockID)
		if err != nil {
			return err
		}
		if current.AnnotatedAt != nil {
			return fmt.Errorf("feedstock %s is already annotated", annotation.FeedstockID)
		}
		if strings.TrimSpace(current.Summary) == "" {
			return fmt.Errorf("feedstock %s must be summarized before annotation", annotation.FeedstockID)
		}
		return guard.Mutate(func() error {
			typeEntries, _, err := dataStore.LoadMasters("types")
			if err != nil {
				return err
			}
			types, err := domain.NormalizeKnowledgeTypes(annotation.Types)
			if err != nil {
				return err
			}
			vocabulary := domain.NewVocabulary(typeEntries, nil)
			for _, value := range types {
				if err := vocabulary.ValidateType(value); err != nil {
					return err
				}
			}
			return dataStore.AnnotateFeedstock(annotation.FeedstockID, types, now)
		})
	})
	return 0, err
}

func Summarize(
	ctx context.Context,
	dataStore Repository,
	guard InvocationGuard,
	feedstockID,
	summary string,
) error {
	if guard == nil {
		guard = unrestrictedInvocation{}
	}
	if err := guard.ValidateFeedstock(feedstockID); err != nil {
		return err
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("summary is required")
	}
	feedstock, err := dataStore.GetFeedstock(feedstockID)
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
		current, err := dataStore.GetFeedstock(feedstockID)
		if err != nil {
			return err
		}
		if current.AnnotatedAt != nil {
			return fmt.Errorf("feedstock %s is already annotated", feedstockID)
		}
		if strings.TrimSpace(current.Summary) != "" {
			return fmt.Errorf("feedstock %s is already summarized", feedstockID)
		}
		return guard.Mutate(func() error {
			return dataStore.SummarizeFeedstock(feedstockID, summary)
		})
	})
}
