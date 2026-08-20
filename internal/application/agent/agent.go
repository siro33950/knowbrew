package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/siro33950/knowbrew/internal/domain"
)

type Task string

const (
	TaskDraw            Task = "draw"
	TaskBrew            Task = "brew"
	TaskDistillSelect   Task = "distill_select"
	TaskDistillGenerate Task = "distill_generate"
)

type ReadState struct {
	Subjects          map[string]SubjectReadState
	Inspected         []string
	Submitted         []domain.KnowledgeCandidate
	AnnotationContext bool
}

type SubjectReadState struct {
	Catalog []string
	Digest  string
}

type Runner interface {
	Run(context.Context, Task, string, string) (RunResult, error)
}

type RunResult struct {
	Output json.RawMessage
	Usage  Usage
	Reads  ReadState
}

type knowledgeTypesContextKey struct{}

func WithKnowledgeTypes(ctx context.Context, values []string) context.Context {
	return context.WithValue(ctx, knowledgeTypesContextKey{}, append([]string(nil), values...))
}

func KnowledgeTypesFromContext(ctx context.Context) []string {
	values, _ := ctx.Value(knowledgeTypesContextKey{}).([]string)
	return append([]string(nil), values...)
}

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
