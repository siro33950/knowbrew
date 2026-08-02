package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/siro33950/knowbrew/internal/application/agent"
)

func DecodeResult(data json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode structured result: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode structured result: multiple JSON values")
		}
		return fmt.Errorf("decode structured result: %w", err)
	}
	return nil
}

func knowledgeTypeNames(ctx context.Context, task Task) ([]string, error) {
	if task == TaskSummarize {
		return nil, nil
	}
	return agent.KnowledgeTypesFromContext(ctx), nil
}

func resultSchema(task Task, typeNames []string) map[string]any {
	switch task {
	case TaskSummarize:
		return objectSchema(
			[]string{"summary"},
			map[string]any{"summary": map[string]any{"type": "string", "minLength": 1}},
		)
	case TaskAnnotate:
		return objectSchema(
			[]string{"assertions"},
			map[string]any{
				"assertions": map[string]any{
					"type": "array", "maxItems": 32,
					"items": assertionResultSchema(typeNames, false),
				},
			},
		)
	case TaskBrew:
		assertion := assertionResultSchema(typeNames, false)
		resolution := resolutionResultSchema(typeNames)
		return objectSchema(
			[]string{"verification", "corrected_assertion", "resolution"},
			map[string]any{
				"verification": map[string]any{
					"type": "string", "enum": []string{"verified", "corrected", "rejected"},
				},
				"corrected_assertion": nullableSchema(assertion),
				"resolution":          nullableSchema(resolution),
			},
		)
	default:
		return objectSchema(nil, nil)
	}
}

func resolutionResultSchema(typeNames []string) map[string]any {
	null := map[string]any{"type": "null"}
	ids := func(count int) map[string]any {
		return map[string]any{
			"type": "array", "minItems": count, "maxItems": count,
			"items": map[string]any{"type": "string"},
		}
	}
	draft := objectSchema(
		[]string{"type", "subject", "statement", "rationale", "trigger"},
		map[string]any{
			"type": typeSchema(typeNames), "subject": map[string]any{"type": "string"},
			"statement": map[string]any{"type": "string", "minLength": 1},
			"rationale": map[string]any{"type": "string"},
			"trigger":   map[string]any{"type": "string", "enum": []string{"", "always"}},
		},
	)
	variant := func(kind string, count int, draftSchema map[string]any) map[string]any {
		return objectSchema(
			[]string{"kind", "knowledge_ids", "draft"},
			map[string]any{
				"kind":          map[string]any{"type": "string", "enum": []string{kind}},
				"knowledge_ids": ids(count), "draft": draftSchema,
			},
		)
	}
	return map[string]any{"anyOf": []any{
		variant("new", 0, null),
		variant("equivalent", 1, null),
		variant("conflicts", 1, null),
		variant("complements", 1, draft),
	}}
}

func assertionResultSchema(typeNames []string, includeID bool) map[string]any {
	required := []string{"type", "subject", "statement", "rationale", "trigger"}
	properties := map[string]any{
		"type":      typeSchema(typeNames),
		"subject":   map[string]any{"type": "string"},
		"statement": map[string]any{"type": "string"},
		"rationale": map[string]any{"type": "string"},
		"trigger":   map[string]any{"type": "string", "enum": []string{"", "always"}},
	}
	if includeID {
		required = append([]string{"id"}, required...)
		properties["id"] = map[string]any{"type": "string"}
	}
	return objectSchema(required, properties)
}

func typeSchema(typeNames []string) map[string]any {
	result := map[string]any{"type": "string"}
	if len(typeNames) > 0 {
		result["enum"] = typeNames
	}
	return result
}

func objectSchema(required []string, properties map[string]any) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"required": required, "properties": properties,
	}
}

func nullableSchema(schema map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{schema, map[string]any{"type": "null"}}}
}
