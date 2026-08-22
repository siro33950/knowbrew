package runlock

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeyedClaimPathIsStableAndSharded(t *testing.T) {
	root := t.TempDir()
	claimer := FileClaimer{Root: root, Namespace: "feedstock-claims"}
	digest := sha256.Sum256([]byte("feedstock-key"))
	encoded := fmt.Sprintf("%x", digest)
	path := filepath.Join(
		root, ".knowbrew", "state", "feedstock-claims", encoded[:2], encoded+".lock",
	)

	release, err := claimer.Claim(context.Background(), "feedstock-key")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	release, err = claimer.Claim(context.Background(), "feedstock-key")
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("same key did not reuse the same lock file")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

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
