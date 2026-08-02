package domain

import (
	"errors"
	"fmt"
	"slices"
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

func (f *Feedstock) ApplyAnnotation(assertions []Assertion, when time.Time) error {
	if f.AnnotatedAt != nil {
		return fmt.Errorf("feedstock %s is already annotated", f.ID)
	}
	if strings.TrimSpace(f.Summary) == "" {
		return fmt.Errorf("feedstock %s must be summarized before annotation", f.ID)
	}
	if when.IsZero() {
		return errors.New("annotation time is required")
	}
	types, err := AssertionTypes(assertions)
	if err != nil {
		return err
	}
	f.Assertions = append([]Assertion(nil), assertions...)
	f.Types = types
	f.Subjects = AssertionSubjects(assertions)
	f.AnnotatedAt = &when
	return ValidateFeedstock(*f)
}

func (f *Feedstock) ApplyBrewProgress(
	assertions []Assertion,
	brewedAssertions []string,
	when time.Time,
) error {
	if f.AnnotatedAt == nil {
		return fmt.Errorf("feedstock %s is not annotated", f.ID)
	}
	if when.IsZero() {
		return errors.New("brew time is required")
	}
	brewedAssertions = UniqueSorted(brewedAssertions)
	if err := ValidateBrewedAssertions(assertions, brewedAssertions); err != nil {
		return err
	}
	types, err := AssertionTypes(assertions)
	if err != nil {
		return err
	}
	f.Assertions = append([]Assertion(nil), assertions...)
	f.Types = types
	f.Subjects = AssertionSubjects(assertions)
	f.BrewedAssertions = brewedAssertions
	processed := make(map[string]struct{}, len(brewedAssertions))
	for _, assertionID := range brewedAssertions {
		processed[assertionID] = struct{}{}
	}
	complete := true
	for _, assertion := range assertions {
		if assertion.Subject == "" {
			continue
		}
		if _, exists := processed[assertion.ID]; !exists {
			complete = false
			break
		}
	}
	if complete && (len(assertions) == 0 || len(brewedAssertions) > 0) {
		if f.BrewedAt == nil {
			f.BrewedAt = &when
		}
	} else {
		f.BrewedAt = nil
	}
	return ValidateFeedstock(*f)
}

func (f Feedstock) PendingAssertions() []Assertion {
	if f.AnnotatedAt == nil {
		return nil
	}
	processed := make(map[string]struct{}, len(f.BrewedAssertions))
	for _, assertionID := range f.BrewedAssertions {
		processed[assertionID] = struct{}{}
	}
	result := make([]Assertion, 0, len(f.Assertions))
	for _, assertion := range f.Assertions {
		if assertion.Subject == "" || slices.Contains(f.BrewedAssertions, assertion.ID) {
			continue
		}
		if _, exists := processed[assertion.ID]; !exists {
			result = append(result, assertion)
		}
	}
	return result
}
