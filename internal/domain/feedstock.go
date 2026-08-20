package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (f *Feedstock) ApplySummary(summary string) error {
	if f.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already annotated", f.ID)
	}
	if strings.TrimSpace(f.Summary) != "" {
		return fmt.Errorf("feedstock %s is already summarized", f.ID)
	}
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return errors.New("summary is required")
	}
	f.Summary = summary
	return nil
}

func (f *Feedstock) ApplyAnnotation(types []KnowledgeType, when time.Time) error {
	if f.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already annotated", f.ID)
	}
	if strings.TrimSpace(f.Summary) == "" {
		return fmt.Errorf("feedstock %s must be summarized before annotation", f.ID)
	}
	if when.IsZero() {
		return errors.New("annotation time is required")
	}
	types, err := NormalizeKnowledgeTypes(types)
	if err != nil {
		return err
	}
	f.Types = types
	f.AnnotatedAt = &when
	return ValidateFeedstock(*f)
}

func (f *Feedstock) ApplyBrewProgress(when time.Time) error {
	if f.AnnotatedAt == nil {
		return fmt.Errorf("feedstock %s is not annotated", f.ID)
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
