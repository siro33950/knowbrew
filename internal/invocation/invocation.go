package invocation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/fsutil"
)

func ValidateFeedstock(feedstockID string) error {
	expected := strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment))
	if expected != "" && feedstockID != expected {
		return fmt.Errorf("feedstock %s does not match invocation feedstock %s", feedstockID, expected)
	}
	return nil
}

func ValidateSources(sources []string) error {
	expected := strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment))
	if expected == "" {
		return nil
	}
	for _, source := range sources {
		if source == expected {
			return nil
		}
	}
	return fmt.Errorf("operation must cite invocation feedstock %s", expected)
}

// Claim records the single successful mutation permitted for an LLM invocation.
// Call Rollback if the mutation fails so that the backend may retry a corrected
// command during the same invocation.
func Claim(root string) (string, error) {
	id := strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment))
	if id == "" {
		return "", nil
	}
	if err := domain.ValidateIdentifier(id, "invocation ID"); err != nil {
		return "", err
	}
	path, err := fsutil.ResolveWithin(root, ".state", "runs", id+".operation")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create invocation state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", errors.New("this LLM invocation has already completed a knowledge operation")
	}
	if err != nil {
		return "", fmt.Errorf("claim LLM invocation: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close LLM invocation claim: %w", closeErr)
	}
	return path, nil
}

func Rollback(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}

func Cleanup(root, id string) {
	path, ok := claimPath(root, id)
	if !ok {
		return
	}
	_ = os.Remove(path)
}

func Completed(root, id string) bool {
	path, ok := claimPath(root, id)
	if !ok {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func claimPath(root, id string) (string, bool) {
	if id == "" || domain.ValidateIdentifier(id, "invocation ID") != nil {
		return "", false
	}
	path, err := fsutil.ResolveWithin(root, ".state", "runs", id+".operation")
	return path, err == nil
}
