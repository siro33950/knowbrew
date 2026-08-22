package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

func (f *Feedstock) ApplyDraft(summary string, types []KnowledgeType, when time.Time) error {
	if f.DraftedAt != nil {
		return fmt.Errorf("feedstock %s is already drafted", f.ID)
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
	f.DraftedAt = &when
	return ValidateFeedstock(*f)
}

func (f *Feedstock) ApplyExtractionProgress(when time.Time) error {
	if f.DraftedAt == nil {
		return fmt.Errorf("feedstock %s is not drafted", f.ID)
	}
	if when.IsZero() {
		return errors.New("extraction time is required")
	}
	if f.ExtractedAt == nil {
		f.ExtractedAt = &when
	}
	return ValidateFeedstock(*f)
}

func (f Feedstock) PendingExtraction() bool {
	return f.DraftedAt != nil && f.ExtractedAt == nil
}
