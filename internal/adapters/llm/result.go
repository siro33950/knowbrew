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
	if task == TaskSummarize || task == TaskDistillSelect || task == TaskDistillGenerate {
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
			[]string{"types"},
			map[string]any{
				"types": map[string]any{
					"type": "array", "uniqueItems": true,
					"items": typeSchema(typeNames),
				},
			},
		)
	case TaskBrew:
		return objectSchema(
			[]string{"registered"},
			map[string]any{
				"registered": map[string]any{"type": "integer", "minimum": 0},
			},
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
