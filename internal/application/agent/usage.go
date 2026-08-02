package agent

import "fmt"

type Usage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
}

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
	standardInput := usage.InputTokens - usage.CachedInputTokens - usage.CacheWriteInputTokens
	if standardInput < 0 {
		standardInput = 0
	}
	return UsageReport{
		Backend: backend, Model: model, TotalInputTokens: usage.InputTokens,
		StandardInputTokens: standardInput, CacheReadInputTokens: usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens, OutputTokens: usage.OutputTokens,
		TotalTokens: usage.TotalTokens(),
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
