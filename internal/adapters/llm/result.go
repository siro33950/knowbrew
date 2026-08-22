package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/siro33950/knowbrew/internal/application/agent"
	"github.com/siro33950/knowbrew/internal/domain"
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
	if task == TaskDistillSelect || task == TaskDistillGenerate {
		return nil, nil
	}
	return agent.KnowledgeTypesFromContext(ctx), nil
}

func resultSchema(task Task, typeNames []string) map[string]any {
	switch task {
	case TaskDraw:
		return objectSchema(
			[]string{"summary", "types"},
			map[string]any{
				"summary": map[string]any{"type": "string", "minLength": 1},
				"types": map[string]any{
					"type":  "array",
					"items": typeSchema(typeNames),
				},
			},
		)
	case TaskExtract:
		return objectSchema(
			[]string{"knowledge"},
			map[string]any{
				"knowledge": map[string]any{
					"type": "array", "maxItems": domain.MaxKnowledgePerFeedstock,
					"items": knowledgeDraftSchema(typeNames),
				},
			},
		)
	case TaskBrew:
		return objectSchema(
			[]string{"actions"},
			map[string]any{"actions": map[string]any{
				"type": "array", "items": organizationActionSchema(typeNames),
			}},
		)
	case TaskDistillSelect:
		return objectSchema(
			[]string{"knowledge_references"},
			map[string]any{"knowledge_references": map[string]any{
				"type": "array", "items": map[string]any{"type": "string"},
			}},
		)
	case TaskDistillGenerate:
		return objectSchema(
			[]string{"body", "knowledge_references"},
			map[string]any{
				"body": map[string]any{"type": "string"},
				"knowledge_references": map[string]any{
					"type": "array", "items": map[string]any{"type": "string"},
				},
			},
		)
	default:
		return objectSchema(nil, nil)
	}
}

func knowledgeDraftSchema(typeNames []string) map[string]any {
	return objectSchema(
		[]string{"type", "subject", "statement", "rationale"},
		map[string]any{
			"type": typeSchema(typeNames), "subject": map[string]any{"type": "string"},
			"statement": map[string]any{"type": "string", "minLength": 1},
			"rationale": map[string]any{"type": "string"},
		},
	)
}

func organizationActionSchema(typeNames []string) map[string]any {
	null := map[string]any{"type": "null"}
	ids := func(count int) map[string]any {
		return map[string]any{
			"type": "array", "minItems": count, "maxItems": count,
			"items": map[string]any{"type": "string"},
		}
	}
	resolution := func(kind string, count int, draft map[string]any) map[string]any {
		return objectSchema(
			[]string{"kind", "knowledge_ids", "draft"},
			map[string]any{
				"kind":          map[string]any{"type": "string", "enum": []string{kind}},
				"knowledge_ids": ids(count), "draft": draft,
			},
		)
	}
	return objectSchema(
		[]string{"knowledge_id", "resolution"},
		map[string]any{
			"knowledge_id": map[string]any{"type": "string"},
			"resolution": map[string]any{"anyOf": []any{
				resolution("discard", 0, null),
				resolution("new", 0, null),
				resolution("equivalent", 1, null),
				resolution("conflicts", 1, null),
				resolution("complements", 1, knowledgeDraftSchema(typeNames)),
			}},
		},
	)
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
