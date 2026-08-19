package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const MaxAssertionsPerFeedstock = 32

type AssertionDraft struct {
	Type      KnowledgeType
	Subject   string
	Statement string
	Rationale string
}

type VerificationStatus string

const (
	VerificationVerified  VerificationStatus = "verified"
	VerificationCorrected VerificationStatus = "corrected"
	VerificationRejected  VerificationStatus = "rejected"
)

type Vocabulary struct {
	types    map[KnowledgeType]struct{}
	subjects map[string]struct{}
}

func NewVocabulary(types, subjects []MasterEntry) Vocabulary {
	vocabulary := Vocabulary{
		types:    make(map[KnowledgeType]struct{}, len(types)),
		subjects: make(map[string]struct{}, len(subjects)),
	}
	for _, entry := range types {
		vocabulary.types[KnowledgeType(MasterName(entry.Name))] = struct{}{}
	}
	for _, entry := range subjects {
		vocabulary.subjects[MasterName(entry.Name)] = struct{}{}
	}
	return vocabulary
}

func (v Vocabulary) ValidateType(value KnowledgeType) error {
	value = KnowledgeType(strings.TrimSpace(string(value)))
	if err := ValidateKnowledgeTypeName(value); err != nil {
		return err
	}
	if _, exists := v.types[value]; !exists {
		return fmt.Errorf("knowledge type %q is not defined in masters/types", value)
	}
	return nil
}

func (v Vocabulary) ValidateSubject(value string) error {
	value = MasterName(value)
	if value == "" {
		return nil
	}
	if err := ValidateIdentifier(value, "assertion subject"); err != nil {
		return err
	}
	if _, exists := v.subjects[value]; !exists {
		return fmt.Errorf("subject %q is not defined in masters/subjects", value)
	}
	return nil
}

func BuildAssertions(
	feedstockID string,
	drafts []AssertionDraft,
	vocabulary Vocabulary,
) ([]Assertion, error) {
	if len(drafts) > MaxAssertionsPerFeedstock {
		return nil, fmt.Errorf("at most %d assertions are allowed per feedstock", MaxAssertionsPerFeedstock)
	}
	assertions := make([]Assertion, 0, len(drafts))
	seenStatements := make(map[string]struct{}, len(drafts))
	for index, draft := range drafts {
		assertion, err := NewAssertion(feedstockID, draft, vocabulary)
		if err != nil {
			return nil, fmt.Errorf("assertion %d: %w", index+1, err)
		}
		statementKey := strings.ToLower(assertion.Statement) + "\x00" + assertion.Subject
		if _, exists := seenStatements[statementKey]; exists {
			return nil, fmt.Errorf("assertion %d duplicates another statement", index+1)
		}
		seenStatements[statementKey] = struct{}{}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func NewAssertion(feedstockID string, draft AssertionDraft, vocabulary Vocabulary) (Assertion, error) {
	if err := ValidateIdentifier(feedstockID, "feedstock ID"); err != nil {
		return Assertion{}, err
	}
	draft.Type = KnowledgeType(strings.TrimSpace(string(draft.Type)))
	draft.Subject = MasterName(draft.Subject)
	draft.Statement = strings.TrimSpace(draft.Statement)
	draft.Rationale = strings.TrimSpace(draft.Rationale)
	if err := vocabulary.ValidateType(draft.Type); err != nil {
		return Assertion{}, fmt.Errorf("type: %w", err)
	}
	if err := vocabulary.ValidateSubject(draft.Subject); err != nil {
		return Assertion{}, err
	}
	if draft.Statement == "" {
		return Assertion{}, errors.New("statement is required")
	}
	if strings.ContainsAny(draft.Statement, "\r\n") {
		return Assertion{}, errors.New("statement must be one line")
	}
	if strings.Contains(draft.Rationale, "\n\n### ") ||
		strings.Contains(draft.Rationale, "\n\n#### Rationale\n") {
		return Assertion{}, errors.New("rationale contains a reserved heading")
	}
	assertion := Assertion{
		Type: draft.Type, Subject: draft.Subject, Statement: draft.Statement,
		Rationale: draft.Rationale,
	}
	assertion.ID = AssertionID(feedstockID, assertion)
	return assertion, nil
}

func AssertionID(feedstockID string, assertion Assertion) string {
	payload, _ := json.Marshal(struct {
		FeedstockID string        `json:"feedstock_id"`
		Type        KnowledgeType `json:"type"`
		Subject     string        `json:"subject"`
		Statement   string        `json:"statement"`
		Rationale   string        `json:"rationale,omitempty"`
	}{
		FeedstockID: feedstockID, Type: assertion.Type, Subject: assertion.Subject,
		Statement: assertion.Statement, Rationale: assertion.Rationale,
	})
	digest := sha256.Sum256(payload)
	return "as-" + hex.EncodeToString(digest[:16])
}

func AssertionTypes(assertions []Assertion) ([]KnowledgeType, error) {
	values := make([]KnowledgeType, 0, len(assertions))
	for _, assertion := range assertions {
		values = append(values, assertion.Type)
	}
	return NormalizeKnowledgeTypes(values)
}

func AssertionSubjects(assertions []Assertion) []string {
	values := make([]string, 0, len(assertions))
	for _, assertion := range assertions {
		if assertion.Subject != "" {
			values = append(values, assertion.Subject)
		}
	}
	return NormalizeMasterNames(values)
}

func ValidateBrewedAssertions(assertions []Assertion, brewed []string) error {
	ids := make(map[string]struct{}, len(assertions))
	for _, assertion := range assertions {
		ids[assertion.ID] = struct{}{}
	}
	normalized := UniqueSorted(brewed)
	if !slices.Equal(normalized, brewed) {
		return errors.New("feedstock brewed_assertions must be unique and sorted")
	}
	for _, id := range brewed {
		if _, exists := ids[id]; !exists {
			return fmt.Errorf("brewed assertion %q does not exist in feedstock", id)
		}
	}
	return nil
}

func VerifyAssertion(
	current Assertion,
	status VerificationStatus,
	corrected *Assertion,
	hasResolution bool,
	vocabulary Vocabulary,
) (Assertion, bool, error) {
	switch status {
	case VerificationVerified:
		if corrected != nil || !hasResolution {
			return Assertion{}, false, errors.New("verified requires a resolution and no corrected assertion")
		}
		return current, false, nil
	case VerificationCorrected:
		if corrected == nil || !hasResolution {
			return Assertion{}, false, errors.New("corrected requires a corrected assertion and resolution")
		}
		value := *corrected
		value.ID = current.ID
		value.Type = KnowledgeType(strings.TrimSpace(string(value.Type)))
		value.Subject = MasterName(value.Subject)
		value.Statement = strings.TrimSpace(value.Statement)
		value.Rationale = strings.TrimSpace(value.Rationale)
		if value.Subject != current.Subject {
			return Assertion{}, false, errors.New("brew cannot change an assertion subject")
		}
		if err := vocabulary.ValidateType(value.Type); err != nil {
			return Assertion{}, false, err
		}
		if value.Statement == "" || strings.ContainsAny(value.Statement, "\r\n") {
			return Assertion{}, false, errors.New("corrected assertion statement must be one non-empty line")
		}
		if strings.Contains(value.Rationale, "\n\n### ") ||
			strings.Contains(value.Rationale, "\n\n#### Rationale\n") {
			return Assertion{}, false, errors.New("corrected assertion rationale contains a reserved heading")
		}
		return value, false, nil
	case VerificationRejected:
		if corrected != nil || hasResolution {
			return Assertion{}, false, errors.New("rejected does not accept corrected assertion or resolution")
		}
		return current, true, nil
	default:
		return Assertion{}, false, fmt.Errorf("invalid verification %q", status)
	}
}
