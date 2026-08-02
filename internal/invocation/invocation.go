package invocation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/config"
	"github.com/siro33950/knowbrew/internal/domain"
	"github.com/siro33950/knowbrew/internal/fsutil"
)

type ReadState struct {
	Subject           string   `json:"subject,omitempty"`
	Catalog           []string `json:"catalog,omitempty"`
	CatalogDigest     string   `json:"catalog_digest,omitempty"`
	Inspected         []string `json:"inspected,omitempty"`
	AnnotationContext bool     `json:"annotation_context,omitempty"`
}

func RecordAnnotationContext(root string) error {
	state, path, err := readState(root)
	if err != nil {
		return err
	}
	if state.AnnotationContext {
		return errors.New("annotation context has already been loaded for this invocation")
	}
	state.AnnotationContext = true
	return writeState(path, state)
}

func ValidateFeedstock(feedstockID string) error {
	expected := strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment))
	if expected != "" && feedstockID != expected {
		return fmt.Errorf("feedstock %s does not match invocation feedstock %s", feedstockID, expected)
	}
	return nil
}

func ValidateAssertion(assertionID string) error {
	expected := strings.TrimSpace(os.Getenv(config.InvocationAssertionEnvironment))
	if expected != "" && assertionID != expected {
		return fmt.Errorf("assertion %s does not match invocation assertion %s", assertionID, expected)
	}
	return nil
}

func ValidateFeedstocks(feedstocks []string) error {
	expected := strings.TrimSpace(os.Getenv(config.InvocationFeedstockEnvironment))
	if expected == "" {
		return nil
	}
	for _, feedstock := range feedstocks {
		if feedstock == expected {
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
	path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+".operation")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create invocation state directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return "", errors.New("this LLM invocation has already completed an operation")
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
	if id == "" || domain.ValidateIdentifier(id, "invocation ID") != nil {
		return
	}
	for _, suffix := range []string{".operation", ".reads.json"} {
		path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+suffix)
		if err == nil {
			_ = os.Remove(path)
		}
	}
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
	path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+".operation")
	return path, err == nil
}

func RecordCatalog(root, subject string, ids []string, digest string) error {
	state, path, err := readState(root)
	if err != nil {
		return err
	}
	subject = domain.MasterName(subject)
	if state.Subject != "" {
		return fmt.Errorf("invocation already loaded subject catalog %q", state.Subject)
	}
	state.Subject = subject
	state.Catalog = domain.UniqueSorted(ids)
	state.CatalogDigest = strings.TrimSpace(digest)
	return writeState(path, state)
}

func RecordInspected(root string, ids []string) error {
	state, path, err := readState(root)
	if err != nil {
		return err
	}
	state.Inspected = domain.UniqueSorted(append(state.Inspected, ids...))
	return writeState(path, state)
}

func CurrentReadState(root string) (ReadState, error) {
	state, _, err := readState(root)
	return state, err
}

func ReadStateForInvocation(root, id string) (ReadState, error) {
	if id == "" || domain.ValidateIdentifier(id, "invocation ID") != nil {
		return ReadState{}, nil
	}
	path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+".reads.json")
	if err != nil {
		return ReadState{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ReadState{}, nil
	}
	if err != nil {
		return ReadState{}, err
	}
	var state ReadState
	if err := json.Unmarshal(data, &state); err != nil {
		return ReadState{}, err
	}
	return state, nil
}

func readState(root string) (ReadState, string, error) {
	id := strings.TrimSpace(os.Getenv(config.InvocationIDEnvironment))
	if id == "" {
		return ReadState{}, "", errors.New("knowledge reads are available only inside a knowbrew invocation")
	}
	if err := domain.ValidateIdentifier(id, "invocation ID"); err != nil {
		return ReadState{}, "", err
	}
	path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+".reads.json")
	if err != nil {
		return ReadState{}, "", err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ReadState{}, path, nil
	}
	if err != nil {
		return ReadState{}, "", err
	}
	var state ReadState
	if err := json.Unmarshal(data, &state); err != nil {
		return ReadState{}, "", err
	}
	return state, path, nil
}

func writeState(path string, state ReadState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.AtomicWrite(path, append(data, '\n'), 0o600)
}
