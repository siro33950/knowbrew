package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

// Usage is normalized across backends. InputTokens includes cache reads and
// cache writes; those subsets must not be added again.
type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

// UsageReport exposes pricing classes together with backend and model identity.
// StandardInputTokens excludes both cache reads and cache writes.
type UsageReport struct {
	Backend               string `json:"backend"`
	Model                 string `json:"model"`
	TotalInputTokens      int64  `json:"total_input_tokens"`
	StandardInputTokens   int64  `json:"standard_input_tokens"`
	CacheReadInputTokens  int64  `json:"cache_read_input_tokens"`
	CacheWriteInputTokens int64  `json:"cache_write_input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	TotalTokens           int64  `json:"total_tokens"`
}

func (usage Usage) TotalTokens() int64 {
	return usage.InputTokens + usage.OutputTokens
}

func (usage *Usage) Add(other Usage) {
	usage.InputTokens += other.InputTokens
	usage.CachedInputTokens += other.CachedInputTokens
	usage.CacheWriteInputTokens += other.CacheWriteInputTokens
	usage.OutputTokens += other.OutputTokens
}

func NewUsageReport(backend, model string, usage Usage) UsageReport {
	standardInput := usage.InputTokens -
		usage.CachedInputTokens -
		usage.CacheWriteInputTokens
	if standardInput < 0 {
		standardInput = 0
	}
	return UsageReport{
		Backend:               backend,
		Model:                 model,
		TotalInputTokens:      usage.InputTokens,
		StandardInputTokens:   standardInput,
		CacheReadInputTokens:  usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		OutputTokens:          usage.OutputTokens,
		TotalTokens:           usage.TotalTokens(),
	}
}

func FormatUsage(usage Usage) string {
	return fmt.Sprintf(
		"in %s / out %s",
		FormatTokenCount(usage.InputTokens),
		FormatTokenCount(usage.OutputTokens),
	)
}

func FormatTokenCount(tokens int64) string {
	if tokens < 1000 {
		return fmt.Sprintf("%d tokens", tokens)
	}
	value := float64(tokens)
	suffix := "k"
	divisor := 1000.0
	if tokens >= 1_000_000 {
		suffix = "m"
		divisor = 1_000_000
	}
	scaled := value / divisor
	precision := 1
	if scaled >= 100 {
		precision = 0
	}
	return fmt.Sprintf("%.*f%s tokens", precision, scaled, suffix)
}

const usageCaptureLimit = 8 << 20

type usageCapture struct {
	mu   sync.Mutex
	data []byte
}

func (capture *usageCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.data = append(capture.data, data...)
	if len(capture.data) > usageCaptureLimit {
		capture.data = append([]byte(nil), capture.data[len(capture.data)-usageCaptureLimit:]...)
	}
	return len(data), nil
}

func (capture *usageCapture) Usage(backend string) Usage {
	capture.mu.Lock()
	data := append([]byte(nil), capture.data...)
	capture.mu.Unlock()
	switch backend {
	case "claude-cli":
		return parseClaudeUsage(data)
	case "codex-cli":
		return parseCodexUsage(data)
	default:
		return Usage{}
	}
}

func (capture *usageCapture) Bytes() []byte {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]byte(nil), capture.data...)
}

func parseClaudeUsage(data []byte) Usage {
	var response struct {
		Usage struct {
			InputTokens              int64 `json:"input_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(data), &response); err != nil {
		return Usage{}
	}
	return Usage{
		InputTokens: response.Usage.InputTokens +
			response.Usage.CacheCreationInputTokens +
			response.Usage.CacheReadInputTokens,
		CachedInputTokens:     response.Usage.CacheReadInputTokens,
		CacheWriteInputTokens: response.Usage.CacheCreationInputTokens,
		OutputTokens:          response.Usage.OutputTokens,
	}
}

func parseCodexUsage(data []byte) Usage {
	var usage Usage
	for _, line := range bytes.Split(data, []byte("\n")) {
		var event struct {
			Type  string `json:"type"`
			Usage struct {
				InputTokens       int64 `json:"input_tokens"`
				CachedInputTokens int64 `json:"cached_input_tokens"`
				OutputTokens      int64 `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(bytes.TrimSpace(line), &event) != nil ||
			event.Type != "turn.completed" {
			continue
		}
		usage = Usage{
			InputTokens:       event.Usage.InputTokens,
			CachedInputTokens: event.Usage.CachedInputTokens,
			OutputTokens:      event.Usage.OutputTokens,
		}
	}
	return usage
}

func usageFromOpenAI(
	promptTokens,
	completionTokens,
	cachedTokens int64,
) Usage {
	return Usage{
		InputTokens:       promptTokens,
		CachedInputTokens: min(cachedTokens, promptTokens),
		OutputTokens:      completionTokens,
	}
}
