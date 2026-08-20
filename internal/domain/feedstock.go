package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (f *Feedstock) ApplyDraft(summary string, types []KnowledgeType, when time.Time) error {
	if f.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already drawn", f.ID)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("summary is required")
	}
	if when.IsZero() {
		return errors.New("draw time is required")
	}
	types, err := NormalizeKnowledgeTypes(types)
	if err != nil {
		return err
	}
	f.Summary = summary
	f.Types = types
	f.AnnotatedAt = &when
	return ValidateFeedstock(*f)
}

func (f *Feedstock) ApplyBrewProgress(when time.Time) error {
	if f.AnnotatedAt == nil {
		return fmt.Errorf("feedstock %s is not drawn", f.ID)
	}
	if when.IsZero() {
		return errors.New("brew time is required")
	}
	if f.BrewedAt == nil {
		f.BrewedAt = &when
	}
	return ValidateFeedstock(*f)
}

func (f Feedstock) PendingBrew() bool {
	return f.AnnotatedAt != nil && len(f.Types) > 0 && f.BrewedAt == nil
}
