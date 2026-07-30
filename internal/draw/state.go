package draw

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/siro33950/knowbrew/internal/fsutil"
)

type SessionState struct {
	Agent        string    `json:"agent"`
	SessionID    string    `json:"session_id"`
	Path         string    `json:"path"`
	Size         int64     `json:"size"`
	Modified     time.Time `json:"modified"`
	FeedstockIDs []string  `json:"feedstock_ids"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type State struct {
	Sessions map[string]SessionState `json:"sessions"`
}

func stateKey(agent, sessionID string) string {
	return agent + "\x1f" + sessionID
}

func loadState(root string) (State, error) {
	path := filepath.Join(root, ".state", "draw-state.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Sessions: map[string]SessionState{}}, nil
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Sessions == nil {
		state.Sessions = map[string]SessionState{}
	}
	return state, nil
}

func saveState(root string, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.AtomicWrite(filepath.Join(root, ".state", "draw-state.json"), data, 0o600)
}
