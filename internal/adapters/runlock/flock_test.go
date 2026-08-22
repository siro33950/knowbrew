package runlock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestKeyedClaimsWaitForTheSameKeyButNotDifferentKeys(t *testing.T) {
	claimer := FileClaimer{Root: t.TempDir(), Namespace: "subject-claims"}
	releaseFirst, err := claimer.Claim(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := claimer.Claim(context.Background(), "second")
	if err != nil {
		t.Fatalf("different key waited: %v", err)
	}
	if err := releaseSecond(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := claimer.Claim(ctx, "first"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same-key claim error = %v", err)
	}
	if err := releaseFirst(); err != nil {
		t.Fatal(err)
	}
	releaseAgain, err := claimer.Claim(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := releaseAgain(); err != nil {
		t.Fatal(err)
	}
}
