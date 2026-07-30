package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/siro33950/knowbrew/internal/diagnostic"
	"github.com/siro33950/knowbrew/internal/domain"
)

type Parser interface {
	Parse(path string) ([]domain.FeedstockCandidate, []diagnostic.Warning, error)
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

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9._:-]+`)

func FeedstockID(agent, sessionID string, number int) string {
	sessionID = unsafeID.ReplaceAllString(sessionID, "-")
	sessionID = strings.Trim(sessionID, "-")
	if sessionID == "" {
		sessionID = "unknown"
	}
	return fmt.Sprintf("%s-%s-t%06d", agent, sessionID, number)
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

func clipped(value string) string {
	const limit = 4000
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "…"
}
