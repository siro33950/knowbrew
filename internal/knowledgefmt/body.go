package knowledgefmt

import (
	"errors"
	"strings"
)

func Encode(statement, rationale string) (string, error) {
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return "", errors.New("statement is required")
	}
	if strings.ContainsAny(statement, "\r\n") {
		return "", errors.New("statement must be one line")
	}
	body := "## Claim\n\n" + statement
	if rationale = strings.TrimSpace(rationale); rationale != "" {
		body += "\n\n## Rationale\n\n" + rationale
	}
	return body, nil
}

func Decode(body string) (string, string, error) {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "## Claim\n\n") {
		return "", "", errors.New("knowledge body must begin with ## Claim")
	}
	content := strings.TrimPrefix(body, "## Claim\n\n")
	statement, rationale, found := strings.Cut(content, "\n\n## Rationale\n\n")
	statement = strings.TrimSpace(statement)
	if statement == "" {
		return "", "", errors.New("knowledge claim is empty")
	}
	if found {
		rationale = strings.TrimSpace(rationale)
	}
	return statement, rationale, nil
}
