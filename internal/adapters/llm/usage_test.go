package llm

import "testing"

func TestUsageAddsWithoutDoubleCountingCachedInput(t *testing.T) {
	usage := Usage{
		InputTokens: 100, CachedInputTokens: 60,
		CacheWriteInputTokens: 20, OutputTokens: 10,
	}
	usage.Add(Usage{
		InputTokens: 200, CachedInputTokens: 150,
		CacheWriteInputTokens: 50, OutputTokens: 20,
	})
	if usage.InputTokens != 300 || usage.CachedInputTokens != 210 ||
		usage.CacheWriteInputTokens != 70 ||
		usage.OutputTokens != 30 || usage.TotalTokens() != 330 {
		t.Fatalf("usage = %#v, total = %d", usage, usage.TotalTokens())
	}
}

func TestParseClaudeUsageIncludesCacheInputInTotalInput(t *testing.T) {
	usage := parseClaudeUsage([]byte(`{
		"type":"result",
		"usage":{
			"input_tokens":100,
			"cache_creation_input_tokens":200,
			"cache_read_input_tokens":300,
			"output_tokens":40
		}
	}`))
	if usage.InputTokens != 600 || usage.CachedInputTokens != 300 ||
		usage.CacheWriteInputTokens != 200 ||
		usage.OutputTokens != 40 || usage.TotalTokens() != 640 {
		t.Fatalf("usage = %#v, total = %d", usage, usage.TotalTokens())
	}
}

func TestParseCodexUsageReadsTurnCompletedEvent(t *testing.T) {
	usage := parseCodexUsage([]byte(
		"{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n" +
			"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":450,\"cached_input_tokens\":300,\"output_tokens\":50}}\n",
	))
	if usage.InputTokens != 450 || usage.CachedInputTokens != 300 ||
		usage.OutputTokens != 50 || usage.TotalTokens() != 500 {
		t.Fatalf("usage = %#v, total = %d", usage, usage.TotalTokens())
	}
}

func TestUsageReportExposesPricingClasses(t *testing.T) {
	report := NewUsageReport("api", "priced-model", Usage{
		InputTokens: 1000, CachedInputTokens: 600,
		CacheWriteInputTokens: 100, OutputTokens: 80,
	})
	want := UsageReport{
		Backend: "api", Model: "priced-model",
		TotalInputTokens: 1000, StandardInputTokens: 300,
		CacheReadInputTokens: 600, CacheWriteInputTokens: 100,
		OutputTokens: 80, TotalTokens: 1080,
	}
	if report != want {
		t.Fatalf("report = %#v, want %#v", report, want)
	}
}

func TestFormatUsageSeparatesInputAndOutput(t *testing.T) {
	usage := Usage{InputTokens: 1234, OutputTokens: 56}
	if got, want := FormatUsage(usage), "in 1.2k tokens / out 56 tokens"; got != want {
		t.Fatalf("FormatUsage() = %q, want %q", got, want)
	}
}

func TestFormatTokenCount(t *testing.T) {
	for _, test := range []struct {
		tokens int64
		want   string
	}{
		{tokens: 0, want: "0 tokens"},
		{tokens: 999, want: "999 tokens"},
		{tokens: 1234, want: "1.2k tokens"},
		{tokens: 123_456, want: "123k tokens"},
		{tokens: 1_234_567, want: "1.2m tokens"},
	} {
		if got := FormatTokenCount(test.tokens); got != test.want {
			t.Fatalf("FormatTokenCount(%d) = %q, want %q", test.tokens, got, test.want)
		}
	}
}
