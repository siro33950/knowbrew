package progress

import (
	"bytes"
	"strings"
	"testing"
)

func TestNonTTYPrintsPhaseBoundariesAndFixedMessagesOnly(t *testing.T) {
	var output bytes.Buffer
	display := New(&output, false, 0, false)
	display.Start("Classifying · 0/3 · 2 workers")
	display.Update("Classifying · 1/3 · 2 workers")
	display.Errorf("Classification failed · feedstock-1 · backend failed")
	display.Update("Classifying · 3/3 · 2 workers")
	display.Complete("Classification complete · 3/3 feedstocks")

	text := output.String()
	for _, required := range []string{
		"Classifying · 0/3 · 2 workers",
		"Classification failed · feedstock-1 · backend failed",
		"Classification complete · 3/3 feedstocks",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("output does not contain %q:\n%s", required, text)
		}
	}
	for _, forbidden := range []string{
		"Classifying · 1/3 · 2 workers",
		"Classifying · 3/3 · 2 workers",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("non-TTY output contains incremental update %q:\n%s", forbidden, text)
		}
	}
}

func TestTTYRedrawsAfterErrorsAndClipsProgressToWidth(t *testing.T) {
	var output bytes.Buffer
	display := New(&output, true, 18, false)
	display.Start("Classifying · 12/300 · 5 workers")
	display.Errorf("backend failed")
	display.Update("Classifying · 300/300 · 5 workers")
	display.Complete("Classification complete · 300/300 feedstocks")

	text := output.String()
	if !strings.Contains(text, "\r\x1b[2K") {
		t.Fatalf("TTY output did not redraw in place: %q", text)
	}
	if !strings.Contains(text, "backend failed\n") {
		t.Fatalf("fixed error is missing: %q", text)
	}
	if strings.Contains(text, "Classifying · 12/300 · 5 workers") ||
		strings.Contains(text, "Classification complete · 300/300 feedstocks") {
		t.Fatalf("terminal-width clipping was not applied: %q", text)
	}
	if got := len([]rune(clip("Classifying · 12/300 · 5 workers", 18))); got >= 18 {
		t.Fatalf("clipped line width = %d, want less than 18", got)
	}
}
