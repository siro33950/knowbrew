package domain

import (
	"strings"
	"testing"
	"time"
)

func TestValidateFeedstockAllowsClassificationFieldsWithoutAnnotatedAt(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Feedstock)
	}{
		{
			name: "summary",
			change: func(feedstock *Feedstock) {
				feedstock.Summary = "A classification summary."
			},
		},
		{
			name: "types",
			change: func(feedstock *Feedstock) {
				feedstock.Types = []KnowledgeType{"property"}
			},
		},
		{
			name: "assertions",
			change: func(feedstock *Feedstock) {
				feedstock.Assertions = []Assertion{{
					ID: "as-one", Type: "property",
					Statement: "The value is stable.",
				}}
			},
		},
		{
			name: "all classification fields",
			change: func(feedstock *Feedstock) {
				feedstock.Summary = "A classification summary."
				feedstock.Types = []KnowledgeType{"property"}
				feedstock.Assertions = []Assertion{{
					ID: "as-one", Type: "property",
					Statement: "The value is stable.",
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			feedstock := validFeedstockForValidation()
			test.change(&feedstock)
			if err := ValidateFeedstock(feedstock); err != nil {
				t.Fatalf("ValidateFeedstock() error = %v", err)
			}
		})
	}
}

func TestValidateFeedstockRejectsBrewedAtWithoutAnnotatedAt(t *testing.T) {
	feedstock := validFeedstockForValidation()
	brewedAt := time.Now().UTC()
	feedstock.BrewedAt = &brewedAt
	err := ValidateFeedstock(feedstock)
	if err == nil || !strings.Contains(err.Error(), "unannotated feedstock must not have brewed_at") {
		t.Fatalf("ValidateFeedstock() error = %v", err)
	}
}

func TestValidateAnnotatedFeedstockRequirementsRemain(t *testing.T) {
	annotatedAt := time.Now().UTC()
	tests := []struct {
		name      string
		change    func(*Feedstock)
		wantError string
	}{
		{
			name: "summary required",
			change: func(feedstock *Feedstock) {
				feedstock.AnnotatedAt = &annotatedAt
			},
			wantError: "annotated feedstock summary is required",
		},
		{
			name: "types must be derived from assertions",
			change: func(feedstock *Feedstock) {
				feedstock.AnnotatedAt = &annotatedAt
				feedstock.Summary = "A classification summary."
				feedstock.Types = []KnowledgeType{"property"}
			},
			wantError: "feedstock types must equal the types derived from assertions",
		},
		{
			name: "assertion type validated",
			change: func(feedstock *Feedstock) {
				feedstock.AnnotatedAt = &annotatedAt
				feedstock.Summary = "A classification summary."
				feedstock.Types = []KnowledgeType{"invalid type"}
				feedstock.Assertions = []Assertion{{
					ID: "as-one", Type: "invalid type",
					Statement: "The value is stable.",
				}}
			},
			wantError: "feedstock types:",
		},
		{
			name: "subjects must be derived from assertions",
			change: func(feedstock *Feedstock) {
				feedstock.AnnotatedAt = &annotatedAt
				feedstock.Summary = "A classification summary."
				feedstock.Types = []KnowledgeType{"property"}
				feedstock.Subjects = []string{"wrong-subject"}
				feedstock.Assertions = []Assertion{{
					ID: "as-one", Type: "property", Subject: "right-subject",
					Statement: "The value is stable.",
				}}
			},
			wantError: "feedstock subjects must equal the subjects derived from assertions",
		},
		{
			name: "valid annotated feedstock",
			change: func(feedstock *Feedstock) {
				feedstock.AnnotatedAt = &annotatedAt
				feedstock.Summary = "A classification summary."
				feedstock.Types = []KnowledgeType{"property"}
				feedstock.Assertions = []Assertion{{
					ID: "as-one", Type: "property",
					Statement: "The value is stable.",
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			feedstock := validFeedstockForValidation()
			test.change(&feedstock)
			err := ValidateFeedstock(feedstock)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("ValidateFeedstock() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("ValidateFeedstock() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func validFeedstockForValidation() Feedstock {
	return Feedstock{
		Schema:    SchemaVersion,
		ID:        "fs-validation",
		TurnID:    "turn-validation",
		Session:   SessionRef{ID: "session-validation"},
		Timestamp: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		Agent:     "claude",
	}
}
