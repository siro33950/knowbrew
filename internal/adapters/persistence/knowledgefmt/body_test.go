package knowledgefmt

import (
	"strings"
	"testing"
)

func TestStructuredClaimRoundTrip(t *testing.T) {
	statement := "knowbrewは推論エフォートを次の方法で渡す。\n\n" +
		"- `claude-cli`: `--effort <value>`\n" +
		"- `codex-cli`: `-c model_reasoning_effort=<value>`\n" +
		"- `ollama`: 渡さない"
	rationale := "バックエンドごとに指定方法と対応可否が異なるため。"

	body, err := Encode(statement, rationale)
	if err != nil {
		t.Fatal(err)
	}
	decodedStatement, decodedRationale, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	if decodedStatement != statement || decodedRationale != rationale {
		t.Fatalf(
			"decoded = (%q, %q), want (%q, %q)",
			decodedStatement, decodedRationale, statement, rationale,
		)
	}
}

func TestEncodeRejectsRationaleHeadingInsideClaim(t *testing.T) {
	_, err := Encode("claim\n\n## Rationale\n\nnot rationale", "")
	if err == nil || !strings.Contains(err.Error(), "Rationale heading") {
		t.Fatalf("error = %v, want reserved heading error", err)
	}
}
