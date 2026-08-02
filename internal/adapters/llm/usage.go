package llm

import (
	"bytes"
	"encoding/json"
	"sync"

	"github.com/siro33950/knowbrew/internal/application/agent"
)

type Usage = agent.Usage
type UsageReport = agent.UsageReport

var NewUsageReport = agent.NewUsageReport
var FormatUsage = agent.FormatUsage
var FormatTokenCount = agent.FormatTokenCount

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
