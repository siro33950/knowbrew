package embedding

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/siro33950/knowbrew/internal/adapters/config"
)

func TestManagedEmbedding(t *testing.T) {
	root := os.Getenv("KNOWBREW_TEST_MANAGED_EMBEDDING_ROOT")
	if root == "" {
		t.Skip("set KNOWBREW_TEST_MANAGED_EMBEDDING_ROOT to run managed model validation")
	}
	model := os.Getenv("KNOWBREW_TEST_MANAGED_EMBEDDING_MODEL")
	if model == "" {
		model = config.EmbeddingRuri
	}
	dimension := map[string]int{
		config.EmbeddingRuri:      512,
		config.EmbeddingSnowflake: 768,
		config.EmbeddingQwen:      1024,
	}[model]
	if dimension == 0 {
		t.Fatalf("unknown managed model %q", model)
	}
	if err := Prepare(context.Background(), root, model, os.Stderr); err != nil {
		t.Fatal(err)
	}
	encoder, err := Open(root, config.Embedding{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := encoder.Close(); err != nil {
			t.Errorf("close encoder: %v", err)
		}
	})
	query, err := encoder.EncodeQuery(context.Background(), "検索の順位を意味と文字列で統合する")
	if err != nil {
		t.Fatal(err)
	}
	documents, err := encoder.EncodeDocuments(context.Background(), []string{
		"全文検索とベクトル検索の順位をRRFで統合する。",
		"明日の天気予報を確認する。",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(query) != dimension || len(documents[0]) != dimension {
		t.Fatalf("dimensions = query:%d document:%d", len(query), len(documents[0]))
	}
	if cosine(documents[0], documents[1]) > 0.999999 {
		t.Fatalf("distinct documents produced the same embedding")
	}
	if norm := cosine(query, query); math.Abs(norm-1) > 0.0001 {
		t.Fatalf("query vector norm = %f", norm)
	}
}

func cosine(left, right []float32) float64 {
	var result float64
	for index := range left {
		result += float64(left[index]) * float64(right[index])
	}
	return math.Max(-1, math.Min(1, result))
}
