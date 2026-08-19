package domain

import "testing"

// The golden value pins the identifier scheme: recomputing the ID of an
// existing assertion must keep returning the stored value.
func TestAssertionIDMatchesGoldenHash(t *testing.T) {
	id := AssertionID("fs-golden", Assertion{
		Type: "property", Subject: "knowbrew",
		Statement: "Assertion IDs stay stable.", Rationale: "Golden.",
	})
	if id != "as-71040dc8130763f4ac52f8c38c9ccf5d" {
		t.Fatalf("AssertionID = %s", id)
	}
}

func TestSubjectNameFromSourceUsesRepositoryBasename(t *testing.T) {
	tests := map[string]string{
		"ssh://git@github.com/example/knowbrew.git": "knowbrew",
		"git@github.com:example/knowbrew.git":       "knowbrew",
		"/workspace/knowbrew.worktrees/feature":     "feature",
	}
	for input, want := range tests {
		if got := SubjectNameFromSource(input); got != want {
			t.Errorf("SubjectNameFromSource(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAliasMatchNormalizesRepositoryURLs(t *testing.T) {
	if !AliasMatch("git@github.com:example/knowbrew.git", "https://github.com/example/knowbrew.git") {
		t.Fatal("equivalent repository URLs did not match")
	}
	if AliasMatch("git@github.com:first/knowbrew.git", "https://github.com/second/knowbrew.git") {
		t.Fatal("different repository owners were conflated")
	}
}
