package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

type Encoder interface {
	ID() string
	Dimension() int
	EncodeDocuments(context.Context, []string) ([][]float32, error)
	EncodeQuery(context.Context, string) ([]float32, error)
	Close() error
}

type Manifest struct {
	ID             string   `json:"id"`
	Backend        string   `json:"backend"`
	Dimension      int      `json:"dimension"`
	MaxLength      int      `json:"max_length"`
	ModelFile      string   `json:"model_file"`
	TokenizerFile  string   `json:"tokenizer_file,omitempty"`
	RuntimeFile    string   `json:"runtime_file,omitempty"`
	ExecutableFile string   `json:"executable_file,omitempty"`
	InputNames     []string `json:"input_names,omitempty"`
	OutputName     string   `json:"output_name,omitempty"`
	Pooling        string   `json:"pooling,omitempty"`
	QueryPrefix    string   `json:"query_prefix,omitempty"`
	DocumentPrefix string   `json:"document_prefix,omitempty"`
}

func Open(root string, configured config.Embedding) (Encoder, error) {
	model := strings.TrimSpace(configured.Model)
	if model == "" || model == config.EmbeddingDisabled {
		return nil, nil
	}
	path := strings.TrimSpace(configured.Path)
	if model != config.EmbeddingCustom {
		path = filepath.Join(root, ".knowbrew", "state", "models", model)
	}
	manifest, err := ReadManifest(path)
	if err != nil {
		return nil, fmt.Errorf("open embedding model %q: %w", model, err)
	}
	switch manifest.Backend {
	case "onnx":
		return newONNXEncoder(path, manifest)
	case "llama.cpp":
		return newLlamaEncoder(path, manifest)
	default:
		return nil, fmt.Errorf("embedding model %q has unsupported backend %q", model, manifest.Backend)
	}
}

func ReadManifest(directory string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if manifest.ID == "" || manifest.Dimension < 1 || manifest.ModelFile == "" {
		return Manifest{}, errors.New("manifest requires id, dimension, and model_file")
	}
	for _, relative := range []string{
		manifest.ModelFile, manifest.TokenizerFile, manifest.RuntimeFile, manifest.ExecutableFile,
	} {
		if relative == "" {
			continue
		}
		path := filepath.Join(directory, filepath.FromSlash(relative))
		if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(directory)+string(filepath.Separator)) {
			return Manifest{}, fmt.Errorf("manifest path escapes model directory: %s", relative)
		}
		if _, err := os.Stat(path); err != nil {
			return Manifest{}, fmt.Errorf("required model file %s: %w", relative, err)
		}
	}
	return manifest, nil
}

var (
	ortInitialize sync.Once
	ortInitErr    error
)

type onnxEncoder struct {
	manifest  Manifest
	tokenizer *tokenizer.Tokenizer
	session   *ort.DynamicAdvancedSession
	mu        sync.Mutex
}

func newONNXEncoder(directory string, manifest Manifest) (*onnxEncoder, error) {
	if manifest.TokenizerFile == "" || manifest.RuntimeFile == "" || len(manifest.InputNames) == 0 || manifest.OutputName == "" {
		return nil, errors.New("ONNX manifest requires tokenizer_file, runtime_file, input_names, and output_name")
	}
	runtimePath := filepath.Join(directory, filepath.FromSlash(manifest.RuntimeFile))
	ortInitialize.Do(func() {
		ort.SetSharedLibraryPath(runtimePath)
		ortInitErr = ort.InitializeEnvironment(ort.WithLogLevelError())
	})
	if ortInitErr != nil {
		return nil, fmt.Errorf("initialize ONNX Runtime: %w", ortInitErr)
	}
	tokenizerPath := filepath.Join(directory, filepath.FromSlash(manifest.TokenizerFile))
	tokenizerValue, err := pretrained.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("load tokenizer: %w", err)
	}
	modelPath := filepath.Join(directory, filepath.FromSlash(manifest.ModelFile))
	session, err := ort.NewDynamicAdvancedSession(
		modelPath, manifest.InputNames, []string{manifest.OutputName}, nil,
	)
	if err != nil {
		return nil, fmt.Errorf("load ONNX model: %w", err)
	}
	return &onnxEncoder{manifest: manifest, tokenizer: tokenizerValue, session: session}, nil
}

func (encoder *onnxEncoder) ID() string     { return encoder.manifest.ID }
func (encoder *onnxEncoder) Dimension() int { return encoder.manifest.Dimension }

func (encoder *onnxEncoder) Close() error {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if encoder.session == nil {
		return nil
	}
	err := encoder.session.Destroy()
	encoder.session = nil
	return err
}

func (encoder *onnxEncoder) EncodeDocuments(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	result := make([][]float32, len(texts))
	for index, text := range texts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		vector, err := encoder.encode(encoder.manifest.DocumentPrefix + text)
		if err != nil {
			return nil, fmt.Errorf("embed document %d: %w", index+1, err)
		}
		result[index] = vector
	}
	return result, nil
}

func (encoder *onnxEncoder) EncodeQuery(ctx context.Context, text string) ([]float32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return encoder.encode(encoder.manifest.QueryPrefix + text)
}

func (encoder *onnxEncoder) encode(text string) ([]float32, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if encoder.session == nil {
		return nil, errors.New("ONNX embedding encoder is closed")
	}
	encoded, err := encoder.tokenizer.EncodeSingle(text, true)
	if err != nil {
		return nil, err
	}
	limit := encoder.manifest.MaxLength
	if limit <= 0 || limit > len(encoded.Ids) {
		limit = len(encoded.Ids)
	}
	if limit == 0 {
		return nil, errors.New("tokenizer returned no tokens")
	}
	ids := make([]int64, limit)
	mask := make([]int64, limit)
	types := make([]int64, limit)
	for index := range limit {
		ids[index] = int64(encoded.Ids[index])
		mask[index] = 1
		if index < len(encoded.AttentionMask) {
			mask[index] = int64(encoded.AttentionMask[index])
		}
		if index < len(encoded.TypeIds) {
			types[index] = int64(encoded.TypeIds[index])
		}
	}
	shape := ort.NewShape(1, int64(limit))
	inputs := make([]ort.Value, 0, len(encoder.manifest.InputNames))
	for _, name := range encoder.manifest.InputNames {
		var values []int64
		switch name {
		case "input_ids":
			values = ids
		case "attention_mask":
			values = mask
		case "token_type_ids":
			values = types
		default:
			return nil, fmt.Errorf("unsupported ONNX input %q", name)
		}
		tensor, err := ort.NewTensor(shape, values)
		if err != nil {
			destroyValues(inputs)
			return nil, err
		}
		inputs = append(inputs, tensor)
	}
	defer destroyValues(inputs)
	outputs := []ort.Value{nil}
	if err := encoder.session.Run(inputs, outputs); err != nil {
		return nil, err
	}
	defer destroyValues(outputs)
	output, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("ONNX output has type %T, want float32 tensor", outputs[0])
	}
	shapeOut := output.GetShape()
	values := output.GetData()
	var vector []float32
	switch len(shapeOut) {
	case 2:
		if int(shapeOut[len(shapeOut)-1]) != encoder.manifest.Dimension {
			return nil, fmt.Errorf("ONNX output dimension is %d, want %d", shapeOut[len(shapeOut)-1], encoder.manifest.Dimension)
		}
		vector = append([]float32(nil), values[:encoder.manifest.Dimension]...)
	case 3:
		dimension := int(shapeOut[2])
		sequence := int(shapeOut[1])
		if dimension != encoder.manifest.Dimension {
			return nil, fmt.Errorf("ONNX output dimension is %d, want %d", dimension, encoder.manifest.Dimension)
		}
		vector = make([]float32, dimension)
		for token := range sequence {
			if token >= len(mask) || mask[token] == 0 {
				continue
			}
			for column := range dimension {
				vector[column] += values[token*dimension+column]
			}
		}
	default:
		return nil, fmt.Errorf("ONNX output has unsupported shape %v", shapeOut)
	}
	return normalize(vector)
}

func destroyValues(values []ort.Value) {
	for _, value := range values {
		if value != nil {
			_ = value.Destroy()
		}
	}
}

type llamaEncoder struct {
	manifest  Manifest
	directory string
	mu        sync.Mutex
	server    *exec.Cmd
	done      <-chan error
	baseURL   string
	client    *http.Client
}

func newLlamaEncoder(directory string, manifest Manifest) (*llamaEncoder, error) {
	if manifest.ExecutableFile == "" {
		return nil, errors.New("llama.cpp manifest requires executable_file")
	}
	return &llamaEncoder{
		manifest: manifest, directory: directory,
		client: &http.Client{Timeout: 5 * time.Minute},
	}, nil
}

func (encoder *llamaEncoder) ID() string     { return encoder.manifest.ID }
func (encoder *llamaEncoder) Dimension() int { return encoder.manifest.Dimension }

func (encoder *llamaEncoder) Close() error {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if encoder.server == nil {
		return nil
	}
	err := encoder.server.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		err = nil
	}
	if encoder.done != nil {
		select {
		case <-encoder.done:
		case <-time.After(5 * time.Second):
			if err == nil {
				err = errors.New("timed out stopping llama.cpp embedding server")
			}
		}
	}
	encoder.server = nil
	encoder.done = nil
	return err
}

func (encoder *llamaEncoder) EncodeDocuments(
	ctx context.Context,
	texts []string,
) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	values := make([]string, len(texts))
	for index, text := range texts {
		values[index] = encoder.manifest.DocumentPrefix + text
	}
	return encoder.encode(ctx, values)
}

func (encoder *llamaEncoder) EncodeQuery(ctx context.Context, text string) ([]float32, error) {
	vectors, err := encoder.encode(ctx, []string{encoder.manifest.QueryPrefix + text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (encoder *llamaEncoder) encode(ctx context.Context, texts []string) ([][]float32, error) {
	encoder.mu.Lock()
	defer encoder.mu.Unlock()
	if err := encoder.ensureServer(ctx); err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{
		"input": texts, "model": encoder.manifest.ID, "encoding_format": "float",
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, encoder.baseURL+"/v1/embeddings", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := encoder.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llama.cpp embeddings returned %s: %s", response.Status, strings.TrimSpace(string(data)))
	}
	var result struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	vectors := make([][]float32, len(texts))
	for _, item := range result.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, fmt.Errorf("llama.cpp returned invalid embedding index %d", item.Index)
		}
		if len(item.Embedding) != encoder.manifest.Dimension {
			return nil, fmt.Errorf("llama.cpp output dimension is %d, want %d", len(item.Embedding), encoder.manifest.Dimension)
		}
		vectors[item.Index], err = normalize(item.Embedding)
		if err != nil {
			return nil, err
		}
	}
	for index, vector := range vectors {
		if vector == nil {
			return nil, fmt.Errorf("llama.cpp omitted embedding %d", index)
		}
	}
	return vectors, nil
}

func (encoder *llamaEncoder) ensureServer(ctx context.Context) error {
	if encoder.server != nil && encoder.server.ProcessState == nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	executable := filepath.Join(encoder.directory, filepath.FromSlash(encoder.manifest.ExecutableFile))
	model := filepath.Join(encoder.directory, filepath.FromSlash(encoder.manifest.ModelFile))
	pooling := strings.TrimSpace(encoder.manifest.Pooling)
	if pooling == "" {
		pooling = "last"
	}
	command := exec.CommandContext(ctx, executable,
		"--model", model, "--embedding", "--pooling", pooling,
		"--host", "127.0.0.1", "--port", fmt.Sprint(port), "--log-disable",
	)
	command.Dir = filepath.Dir(executable)
	if runtime.GOOS == "darwin" {
		command.Env = append(os.Environ(), "DYLD_LIBRARY_PATH="+filepath.Dir(executable))
	}
	var logs bytes.Buffer
	command.Stdout = &logs
	command.Stderr = &logs
	if err := command.Start(); err != nil {
		return err
	}
	encoder.server = command
	encoder.baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	exited := make(chan error, 1)
	encoder.done = exited
	go func() { exited <- command.Wait() }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, encoder.baseURL+"/health", nil)
		response, requestErr := encoder.client.Do(request)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode/100 == 2 {
				return nil
			}
		}
		select {
		case err := <-exited:
			message := strings.TrimSpace(logs.String())
			if err == nil {
				if message == "" {
					return errors.New("llama.cpp embedding server exited before becoming ready")
				}
				return fmt.Errorf("llama.cpp embedding server exited before becoming ready: %s", message)
			}
			if message == "" {
				return fmt.Errorf("llama.cpp embedding server exited: %w", err)
			}
			return fmt.Errorf("llama.cpp embedding server exited: %w: %s", err, message)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	_ = command.Process.Kill()
	message := strings.TrimSpace(logs.String())
	if message == "" {
		return errors.New("llama.cpp embedding server did not become ready")
	}
	return fmt.Errorf("llama.cpp embedding server did not become ready: %s", message)
}

func normalize(vector []float32) ([]float32, error) {
	var squared float64
	for _, value := range vector {
		squared += float64(value) * float64(value)
	}
	if squared == 0 || math.IsNaN(squared) || math.IsInf(squared, 0) {
		return nil, errors.New("embedding vector has invalid norm")
	}
	norm := float32(math.Sqrt(squared))
	result := make([]float32, len(vector))
	for index, value := range vector {
		result[index] = value / norm
	}
	return result, nil
}
