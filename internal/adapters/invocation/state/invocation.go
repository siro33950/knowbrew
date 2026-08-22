package invocation

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/adapters/config"
	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/domain"
)

type ReadState struct {
	AnnotationContext bool `json:"annotation_context,omitempty"`
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

func Cleanup(root, id string) {
	if id == "" || domain.ValidateIdentifier(id, "invocation ID") != nil {
		return
	}
	path, err := fsutil.ResolveWithin(root, ".knowbrew", "state", "runs", id+".reads.json")
	if err == nil {
		_ = os.Remove(path)
	}
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
