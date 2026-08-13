package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Parser interface {
	Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error)
	ParseIncremental(path string, checkpoint *Checkpoint) (IncrementalResult, []diagnostic.Warning, error)
	ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error)
	SessionID(path string) (string, error)
}

type Checkpoint struct {
	Offset       int64           `json:"offset"`
	Line         int             `json:"line"`
	SnapshotSize int64           `json:"snapshot_size"`
	State        json.RawMessage `json:"state"`
}

type IncrementalResult struct {
	Candidates []domain.FeedstockCandidate
	Checkpoint Checkpoint
	Excluded   bool
}

type warningCollector struct {
	path     string
	seen     map[string]struct{}
	warnings []diagnostic.Warning
}

func newWarningCollector(path string) *warningCollector {
	return &warningCollector{path: path, seen: map[string]struct{}{}}
}

func (collector *warningCollector) add(reason string) {
	if _, exists := collector.seen[reason]; exists {
		return
	}
	collector.seen[reason] = struct{}{}
	collector.warnings = append(collector.warnings, diagnostic.Warning{
		Path: collector.path, Reason: reason,
	})
}

func For(name string) (Parser, error) {
	switch name {
	case "claude":
		return Claude{}, nil
	case "codex":
		return Codex{}, nil
	default:
		return nil, fmt.Errorf("unsupported parser %q", name)
	}
}

func FeedstockID(agent, sessionID, turnID string) string {
	digest := sha256.Sum256([]byte(
		"knowbrew-feedstock-v1\x00" + agent + "\x00" + sessionID + "\x00" + turnID,
	))
	return "fs-" + hex.EncodeToString(digest[:16])
}

func sourceTurnID(explicit string, rawRecord []byte) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	digest := sha256.Sum256(rawRecord)
	return "record-" + hex.EncodeToString(digest[:16])
}

func sessionIDFromPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if strings.HasPrefix(base, "rollout-") {
		parts := strings.Split(base, "-")
		if len(parts) >= 8 {
			return strings.Join(parts[len(parts)-5:], "-")
		}
	}
	return base
}

func SessionIDHint(path string) string {
	return sessionIDFromPath(path)
}

func SourceFingerprint(path string) (string, error) {
	digest := sha256.Sum256([]byte("empty JSONL source"))
	err := scanSnapshot(path, func(_ int, raw []byte) (bool, error) {
		digest = sha256.Sum256(raw)
		return false, nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint source %s: %w", path, err)
	}
	return hex.EncodeToString(digest[:]), nil
}

func setSourceOwner(candidates []domain.FeedstockCandidate, sessionID string) {
	for index := range candidates {
		candidates[index].SourceOwnerSessionID = sessionID
	}
}
