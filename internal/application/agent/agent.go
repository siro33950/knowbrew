package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type Task string

const (
	TaskSummarize Task = "summarize"
	TaskAnnotate  Task = "annotate"
	TaskBrew      Task = "brew"
)

type ReadState struct {
	Subject           string
	Catalog           []string
	CatalogDigest     string
	Inspected         []string
	AnnotationContext bool
}

type Runner interface {
	Run(context.Context, Task, string, string) (RunResult, error)
}

type RunResult struct {
	Output json.RawMessage
	Usage  Usage
	Reads  ReadState
}

type assertionContextKey struct{}
type knowledgeTypesContextKey struct{}

func WithAssertion(ctx context.Context, assertionID string) context.Context {
	return context.WithValue(ctx, assertionContextKey{}, strings.TrimSpace(assertionID))
}

func AssertionFromContext(ctx context.Context) string {
	value, _ := ctx.Value(assertionContextKey{}).(string)
	return strings.TrimSpace(value)
}

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
