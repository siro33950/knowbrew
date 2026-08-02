package parser

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Parser interface {
	Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error)
	ExtractTurn(path, turnID string) ([]domain.DialogueMessage, error)
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
