package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/siro33950/knowbrew/internal/domain"
)

func validDocumentTemplate() domain.DocumentTemplate {
	return domain.DocumentTemplate{
		Name: "concept", Description: "Concept document.", Output: "concept.md",
		Purpose: "Explain the subject.", Readers: []string{"New readers"},
		Covers: []string{"Purpose"}, Excludes: []string{"Procedures"},
		Completion: []string{"Grounded"}, Structure: "# {{subject}}\n",
	}
}

func TestSubjectDocumentReferencesRoundTripAndSurviveAliasUpdate(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	created, err := dataStore.EnsureMaster("subjects", domain.MasterEntry{
		Name: "knowbrew", Definition: "Knowledge CLI.", Documents: []string{"concept", "reference"},
	})
	if err != nil || !created {
		t.Fatalf("created = %v, error = %v", created, err)
	}
	path := filepath.Join(dataStore.Root, "masters", "subjects", "knowbrew.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "documents:\n") || strings.Contains(string(data), "templates:\n") {
		t.Fatalf("subject master uses the wrong document declaration:\n%s", data)
	}
	for _, value := range []string{`- "[[concept]]"`, `- "[[reference]]"`} {
		if !strings.Contains(string(data), value) {
			t.Fatalf("subject master does not contain %q:\n%s", value, data)
		}
	}
	if _, err := dataStore.AddSubjectAliases("knowbrew", []string{"repo"}); err != nil {
		t.Fatal(err)
	}
	entries, warnings, err := dataStore.LoadMasters("subjects")
	if err != nil || len(warnings) != 0 || len(entries) != 1 {
		t.Fatalf("subjects = %#v, warnings = %#v, error = %v", entries, warnings, err)
	}
	if strings.Join(entries[0].Documents, ",") != "concept,reference" {
		t.Fatalf("documents = %#v", entries[0].Documents)
	}
}

func TestTemplateAndDistilledDocumentRoundTrip(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	template := validDocumentTemplate()
	templateData := `---
description: Concept document.
output: concept.md
purpose: Explain the subject.
readers:
  - New readers
covers:
  - Purpose
excludes:
  - Procedures
completion:
  - Grounded
---

# {{subject}}
`
	path := filepath.Join(dataStore.Root, "masters", "templates", "concept.md")
	if err := os.WriteFile(path, []byte(templateData), 0o644); err != nil {
		t.Fatal(err)
	}
	templates, warnings, err := dataStore.LoadTemplates()
	if err != nil || len(warnings) != 0 || len(templates) != 1 {
		t.Fatalf("templates = %#v, warnings = %#v, error = %v", templates, warnings, err)
	}
	if templates[0].Name != template.Name || templates[0].Output != template.Output ||
		!strings.Contains(templates[0].Structure, "{{subject}}") {
		t.Fatalf("template = %#v", templates[0])
	}

	document := domain.DistilledDocument{
		Subject: "knowbrew", Template: "concept",
		KnowledgeIDs: []string{"kn-0123456789abcdef"}, Body: "# knowbrew\n\nBody.",
	}
	if err := dataStore.WriteDistilledDocument(template, document); err != nil {
		t.Fatal(err)
	}
	documentPath := filepath.Join(dataStore.Root, "documents", "knowbrew", "concept.md")
	data, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		`subject: "[[knowbrew]]"`, `template: "[[concept]]"`, `- "[[kn-0123456789abcdef]]"`,
	} {
		if !strings.Contains(string(data), value) {
			t.Fatalf("distilled document does not contain %q:\n%s", value, data)
		}
	}
	loaded, exists, err := dataStore.ReadDistilledDocument(template, "knowbrew")
	if err != nil || !exists || loaded.Body != "# knowbrew\n\nBody.\n" ||
		len(loaded.KnowledgeIDs) != 1 || loaded.KnowledgeIDs[0] != "kn-0123456789abcdef" {
		t.Fatalf("loaded = %#v, exists = %v, error = %v", loaded, exists, err)
	}
	deleted, err := dataStore.DeleteDistilledDocument(template, "knowbrew")
	if err != nil || !deleted {
		t.Fatalf("deleted = %v, error = %v", deleted, err)
	}
	if _, err := os.Stat(documentPath); !os.IsNotExist(err) {
		t.Fatalf("document remains: %v", err)
	}
}

func TestReadDistilledDocumentRejectsDuplicateKnowledgeReferences(t *testing.T) {
	dataStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := dataStore.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	template := validDocumentTemplate()
	path, err := dataStore.DistilledDocumentPath(template, "knowbrew")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data := `---
subject: "[[knowbrew]]"
template: "[[concept]]"
knowledge:
  - "[[kn-0123456789abcdef]]"
  - "[[kn-0123456789abcdef]]"
---

# knowbrew
`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	_, exists, err := dataStore.ReadDistilledDocument(template, "knowbrew")
	if !exists || err == nil || !strings.Contains(err.Error(), "duplicate Knowledge reference") {
		t.Fatalf("exists = %v, error = %v", exists, err)
	}
}
