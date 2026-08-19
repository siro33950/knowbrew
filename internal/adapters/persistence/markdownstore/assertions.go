package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/siro33950/knowbrew/internal/domain"
)

const assertionsHeading = "## Assertions"

func encodeAssertions(assertions []domain.Assertion) (string, error) {
	if len(assertions) == 0 {
		return "", nil
	}
	var output strings.Builder
	output.WriteString(assertionsHeading)
	for _, assertion := range assertions {
		if err := validateAssertionMarkdown(assertion); err != nil {
			return "", err
		}
		output.WriteString("\n\n### ")
		output.WriteString(assertion.ID)
		output.WriteString("\n\n- Type: [[")
		output.WriteString(string(assertion.Type))
		output.WriteString("]]\n")
		if assertion.Subject != "" {
			output.WriteString("- Subject: [[")
			output.WriteString(assertion.Subject)
			output.WriteString("]]\n")
		}
		output.WriteByte('\n')
		output.WriteString(assertion.Statement)
		if assertion.Rationale != "" {
			output.WriteString("\n\n#### Rationale\n\n")
			output.WriteString(assertion.Rationale)
		}
	}
	return output.String(), nil
}

func decodeAssertions(body string) ([]domain.Assertion, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, nil
	}
	prefix := assertionsHeading + "\n\n### "
	if !strings.HasPrefix(body, prefix) {
		return nil, errors.New("feedstock body must contain only the generated Assertions section")
	}
	sections := strings.Split(strings.TrimPrefix(body, prefix), "\n\n### ")
	assertions := make([]domain.Assertion, 0, len(sections))
	for index, section := range sections {
		assertion, err := decodeAssertion(section)
		if err != nil {
			return nil, fmt.Errorf("decode assertion %d: %w", index+1, err)
		}
		assertions = append(assertions, assertion)
	}
	return assertions, nil
}

func decodeAssertion(section string) (domain.Assertion, error) {
	id, rest, found := strings.Cut(section, "\n\n")
	if !found {
		return domain.Assertion{}, errors.New("missing assertion metadata")
	}
	assertion := domain.Assertion{ID: strings.TrimSpace(id)}
	metadata, content, found := strings.Cut(rest, "\n\n")
	if !found {
		return domain.Assertion{}, errors.New("missing assertion statement")
	}
	for _, line := range strings.Split(metadata, "\n") {
		switch {
		case strings.HasPrefix(line, "- Type: "):
			assertion.Type = domain.KnowledgeType(domain.MasterName(strings.TrimPrefix(line, "- Type: ")))
		case strings.HasPrefix(line, "- Trigger: "):
			// Retired metadata written by earlier releases; discard the value
			// so existing feedstocks keep decoding.
		case strings.HasPrefix(line, "- Subject: "):
			assertion.Subject = domain.MasterName(strings.TrimPrefix(line, "- Subject: "))
		default:
			return domain.Assertion{}, fmt.Errorf("unsupported metadata line %q", line)
		}
	}
	statement, rationale, hasRationale := strings.Cut(content, "\n\n#### Rationale\n\n")
	assertion.Statement = strings.TrimSpace(statement)
	if hasRationale {
		assertion.Rationale = strings.TrimSpace(rationale)
	}
	if err := validateAssertionMarkdown(assertion); err != nil {
		return domain.Assertion{}, err
	}
	return assertion, nil
}

func validateAssertionMarkdown(assertion domain.Assertion) error {
	if strings.Contains(assertion.Rationale, "\n\n### ") ||
		strings.Contains(assertion.Rationale, "\n\n#### Rationale\n") {
		return fmt.Errorf("assertion %s rationale contains a reserved heading", assertion.ID)
	}
	return nil
}
