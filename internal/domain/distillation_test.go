package domain

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDistillationDomainRules(t *testing.T) {
	template := DocumentTemplate{
		Name: "concept", Description: "Concept document.", Output: "concept.md",
		Purpose: "Explain the subject.", Structure: "# {{subject}}",
	}
	if err := ValidateDocumentTemplate(template); err != nil {
		t.Fatal(err)
	}
	template.Output = "../concept.md"
	if err := ValidateDocumentTemplate(template); err == nil || !strings.Contains(err.Error(), "one Markdown filename") {
		t.Fatalf("invalid output error = %v", err)
	}

	document := DistilledDocument{
		Subject: "knowbrew", Template: "concept",
		KnowledgeIDs: []string{"kn-0123456789abcdef"}, Body: "# knowbrew\n",
	}
	if err := ValidateDistilledDocument(document); err != nil {
		t.Fatal(err)
	}
	document.Body = "---\nsubject: knowbrew\n---\n"
	if err := ValidateDistilledDocument(document); err == nil || !strings.Contains(err.Error(), "must not contain YAML frontmatter") {
		t.Fatalf("frontmatter body error = %v", err)
	}
}

func TestSemanticSubjectExcludesDistillationTemplateAssignments(t *testing.T) {
	subjects := SemanticSubjects([]MasterEntry{{
		Name: "knowbrew", Definition: "Knowledge system.", Documents: []string{"concept"},
	}})
	data, err := json.Marshal(subjects)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "documents") || strings.Contains(string(data), "concept") {
		t.Fatalf("semantic Subject contains distillation settings: %s", data)
	}
}

func TestDistillationKnowledgeEligibilityAndReferenceValidation(t *testing.T) {
	organizedAt := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	active := Knowledge{ID: "kn-active", Subject: "knowbrew", Approved: true, OrganizedAt: &organizedAt}
	if !IsDistillableKnowledge(active, "[[knowbrew]]") {
		t.Fatal("approved current Knowledge should be distillable")
	}
	retired := active
	retired.SupersededBy = "kn-next"
	if IsDistillableKnowledge(retired, "knowbrew") {
		t.Fatal("superseded Knowledge should not be distillable")
	}
	if IsDistillableKnowledge(active, "other") {
		t.Fatal("another Subject should not be distillable")
	}

	allowed := map[string]struct{}{"kn-active": {}}
	ids, err := ValidateKnowledgeReferences([]string{"[[kn-active]]"}, allowed)
	if err != nil || len(ids) != 1 || ids[0] != "kn-active" {
		t.Fatalf("validated IDs = %#v, error = %v", ids, err)
	}
	if _, err := ValidateKnowledgeReferences([]string{"kn-unknown"}, allowed); err == nil ||
		!strings.Contains(err.Error(), "was not supplied") {
		t.Fatalf("unknown reference error = %v", err)
	}
}
