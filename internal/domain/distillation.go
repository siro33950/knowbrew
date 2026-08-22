package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

const (
	InjectAlways  = "always"
	InjectSubject = "subject"
)

type DocumentTemplate struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Output      string   `json:"output"`
	Purpose     string   `json:"purpose"`
	Readers     []string `json:"readers,omitempty"`
	Covers      []string `json:"covers,omitempty"`
	Excludes    []string `json:"excludes,omitempty"`
	Completion  []string `json:"completion,omitempty"`
	// Inject controls session-start injection of this template's documents;
	// it is not part of the distillation prompt payload.
	Inject    string `json:"-"`
	Structure string `json:"structure"`
}

type DistilledDocument struct {
	Subject      string   `json:"subject"`
	Template     string   `json:"template"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	Body         string   `json:"body"`
}

func ValidateDocumentTemplate(template DocumentTemplate) error {
	template.Name = MasterName(template.Name)
	if err := ValidateIdentifier(template.Name, "template name"); err != nil {
		return err
	}
	if strings.TrimSpace(template.Description) == "" {
		return errors.New("template description is required")
	}
	if strings.TrimSpace(template.Purpose) == "" {
		return errors.New("template purpose is required")
	}
	output := strings.TrimSpace(template.Output)
	if output == "" {
		return errors.New("template output is required")
	}
	if filepath.Base(output) != output || filepath.Ext(output) != ".md" {
		return fmt.Errorf("template output %q must be one Markdown filename", output)
	}
	if strings.TrimSpace(template.Structure) == "" {
		return errors.New("template structure is required")
	}
	switch template.Inject {
	case "", InjectAlways, InjectSubject:
	default:
		return fmt.Errorf("template inject %q must be always or subject", template.Inject)
	}
	return nil
}

func ValidateDistilledDocument(document DistilledDocument) error {
	document.Subject = MasterName(document.Subject)
	document.Template = MasterName(document.Template)
	if err := ValidateIdentifier(document.Subject, "document subject"); err != nil {
		return err
	}
	if err := ValidateIdentifier(document.Template, "document template"); err != nil {
		return err
	}
	if len(document.KnowledgeIDs) == 0 {
		return errors.New("distilled document requires at least one Knowledge reference")
	}
	seen := make(map[string]struct{}, len(document.KnowledgeIDs))
	for _, value := range document.KnowledgeIDs {
		id := MasterName(value)
		if err := ValidateKnowledgeID(id); err != nil {
			return err
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate Knowledge reference %q", id)
		}
		seen[id] = struct{}{}
	}
	body := strings.TrimSpace(document.Body)
	if body == "" {
		return errors.New("distilled document body is required")
	}
	if strings.HasPrefix(body, "---") {
		return errors.New("distilled document body must not contain YAML frontmatter")
	}
	return nil
}

func IsDistillableKnowledge(knowledge Knowledge, subject string) bool {
	return knowledge.OrganizedAt != nil && MasterName(knowledge.Subject) == MasterName(subject) &&
		EffectiveKnowledgeStatus(knowledge) == StatusActive
}

func ValidateKnowledgeReferences(values []string, allowed map[string]struct{}) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		id := MasterName(value)
		if err := ValidateKnowledgeID(id); err != nil {
			return nil, err
		}
		if _, exists := allowed[id]; !exists {
			return nil, fmt.Errorf("Knowledge %s was not supplied for this decision", id)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("duplicate Knowledge reference %q", id)
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	slices.Sort(normalized)
	return normalized, nil
}
