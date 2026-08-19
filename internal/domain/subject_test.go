package domain

import "testing"

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
