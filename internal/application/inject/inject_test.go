package inject

import (
	"strings"
	"testing"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type fakeRepository struct {
	subjects  []domain.MasterEntry
	templates []domain.DocumentTemplate
	documents map[string]domain.DistilledDocument
}

func (repo fakeRepository) LoadMasters(string) ([]domain.MasterEntry, []diagnostic.Warning, error) {
	return repo.subjects, nil, nil
}

func (repo fakeRepository) LoadTemplates() ([]domain.DocumentTemplate, []diagnostic.Warning, error) {
	return repo.templates, nil, nil
}

func (repo fakeRepository) ReadDistilledDocument(
	template domain.DocumentTemplate,
	subject string,
) (domain.DistilledDocument, bool, error) {
	document, exists := repo.documents[subject+"/"+template.Name]
	return document, exists, nil
}

func template(name, inject string) domain.DocumentTemplate {
	return domain.DocumentTemplate{
		Name: name, Description: name + " document.", Output: name + ".md",
		Purpose: "Explain.", Structure: "# {{subject}}", Inject: inject,
	}
}

func distilled(subject, templateName, body string) domain.DistilledDocument {
	return domain.DistilledDocument{
		Subject: subject, Template: templateName,
		KnowledgeIDs: []string{"kn-0123456789abcdef"}, Body: body,
	}
}

func TestB015B033BuildInjectsAlwaysAndMatchedSubjectDocuments(t *testing.T) {
	repo := fakeRepository{
		subjects: []domain.MasterEntry{
			{Name: "owner", Definition: "Owner.", Documents: []string{"persona"}},
			{Name: "alpha", Definition: "Alpha.", Documents: []string{"decisions", "concept"}, Aliases: []string{"/work/alpha"}},
			{Name: "beta", Definition: "Beta.", Documents: []string{"decisions"}, Aliases: []string{"/work/beta"}},
		},
		templates: []domain.DocumentTemplate{
			template("persona", domain.InjectAlways),
			template("decisions", domain.InjectSubject),
			template("concept", ""),
		},
		documents: map[string]domain.DistilledDocument{
			"/decisions":      distilled("", "decisions", "# subjectless\n\nMust not be injected."),
			"owner/persona":   distilled("owner", "persona", "# owner\n\nPersona body."),
			"alpha/decisions": distilled("alpha", "decisions", "# alpha\n\nAlpha decisions body."),
			"alpha/concept":   distilled("alpha", "concept", "# alpha\n\nAlpha concept body."),
			"beta/decisions":  distilled("beta", "decisions", "# beta\n\nBeta decisions body."),
		},
	}
	output, warnings, err := Build(repo, "/work/alpha", nil, 2000)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("warnings = %#v, error = %v", warnings, err)
	}
	for _, expected := range []string{
		"untrusted reference data",
		"## Always-injected documents",
		"### owner / persona",
		"Persona body.",
		"## Working context: alpha",
		"### alpha / decisions",
		"Alpha decisions body.",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, output)
		}
	}
	for _, unexpected := range []string{"beta", "concept body", "Must not be injected"} {
		if strings.Contains(output, unexpected) {
			t.Fatalf("output leaked %q:\n%s", unexpected, output)
		}
	}
}

func TestBuildDiscoversRepositoryOnlyWhenCwdMatchesNothing(t *testing.T) {
	repo := fakeRepository{
		subjects: []domain.MasterEntry{
			{
				Name: "alpha", Definition: "Alpha.", Documents: []string{"decisions"},
				Aliases: []string{"https://github.com/example/alpha.git"},
			},
		},
		templates: []domain.DocumentTemplate{template("decisions", domain.InjectSubject)},
		documents: map[string]domain.DistilledDocument{
			"alpha/decisions": distilled("alpha", "decisions", "# alpha\n\nAlpha decisions body."),
		},
	}
	calls := 0
	discover := func() string {
		calls++
		return "git@github.com:example/alpha.git"
	}
	output, _, err := Build(repo, "/somewhere/else", discover, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !strings.Contains(output, "Alpha decisions body.") {
		t.Fatalf("calls = %d, output = %s", calls, output)
	}

	repo.subjects[0].Aliases = []string{"/work/alpha"}
	calls = 0
	if _, _, err := Build(repo, "/work/alpha", discover, 2000); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("repository was discovered despite a cwd match: calls = %d", calls)
	}
}

func TestBuildReturnsEmptyWithoutInjectableDocuments(t *testing.T) {
	repo := fakeRepository{
		subjects: []domain.MasterEntry{
			{Name: "alpha", Definition: "Alpha.", Documents: []string{"concept"}, Aliases: []string{"/work/alpha"}},
		},
		templates: []domain.DocumentTemplate{template("concept", "")},
		documents: map[string]domain.DistilledDocument{
			"alpha/concept": distilled("alpha", "concept", "# alpha\n\nBody."),
		},
	}
	output, _, err := Build(repo, "/work/alpha", nil, 2000)
	if err != nil || output != "" {
		t.Fatalf("output = %q, error = %v", output, err)
	}
}

func TestBuildAppliesDocumentLevelTokenBudget(t *testing.T) {
	repo := fakeRepository{
		subjects: []domain.MasterEntry{
			{Name: "owner", Definition: "Owner.", Documents: []string{"persona", "rules"}},
		},
		templates: []domain.DocumentTemplate{
			template("persona", domain.InjectAlways),
			template("rules", domain.InjectAlways),
		},
		documents: map[string]domain.DistilledDocument{
			"owner/persona": distilled("owner", "persona", "# owner\n\n"+strings.Repeat("A", 2000)),
			"owner/rules":   distilled("owner", "rules", "# owner\n\n"+strings.Repeat("B", 2000)),
		},
	}
	output, _, err := Build(repo, "/anywhere", nil, 700)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "### owner / persona") ||
		strings.Contains(output, "### owner / rules") ||
		!strings.Contains(output, "1 document(s) were omitted (context.max_tokens = 700)") ||
		!strings.Contains(output, "knowbrew document --subject") {
		t.Fatalf("budget output = %s", output)
	}

	truncated, _, err := Build(repo, "/anywhere", nil, 150)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(truncated, truncatedMarker) ||
		!strings.Contains(truncated, "1 document(s) were omitted") {
		t.Fatalf("truncated output = %s", truncated)
	}
}
