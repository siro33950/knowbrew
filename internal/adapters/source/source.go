package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
	"github.com/siro33950/knowbrew/internal/domain"
)

const defaultLookback = 24 * time.Hour

type Gateway struct {
	Configured []applicationsource.Configured
	catalog    *sessionCatalog
	cache      *sourceCache
}

type sessionCatalog struct {
	once  sync.Once
	files map[string][]applicationsource.File
	err   error
}

func New(configured []applicationsource.Configured) Gateway {
	return newGateway(configured, nil)
}

func NewCached(root string, configured []applicationsource.Configured) Gateway {
	return newGateway(configured, newSourceCache(root))
}

func newGateway(
	configured []applicationsource.Configured,
	cache *sourceCache,
) Gateway {
	copied := make([]applicationsource.Configured, len(configured))
	for index, source := range configured {
		copied[index] = source
		copied[index].Paths = append([]string(nil), source.Paths...)
	}
	return Gateway{Configured: copied, catalog: &sessionCatalog{}, cache: cache}
}

func (Gateway) Collect(
	configured []applicationsource.Configured,
	options applicationsource.Selection,
	now time.Time,
) ([]applicationsource.File, error) {
	if len(options.Sources) > 0 && len(options.Paths) > 0 {
		return nil, errors.New("--source cannot be used with explicit paths")
	}
	if options.MaxTurns < 0 {
		return nil, errors.New("--max must be greater than zero")
	}
	if options.ModifiedSince != nil && options.ModifiedUntil != nil &&
		options.ModifiedSince.After(*options.ModifiedUntil) {
		return nil, errors.New("--since must not be after --until")
	}
	wantedSources := make(map[string]struct{}, len(options.Sources))
	for _, name := range options.Sources {
		name = strings.TrimSpace(name)
		if name != "claude" && name != "codex" {
			return nil, fmt.Errorf("unsupported draw source %q", name)
		}
		wantedSources[name] = struct{}{}
	}
	var inputs []applicationsource.File
	if len(options.Paths) == 0 {
		var err error
		inputs, err = configuredFiles(configured, wantedSources)
		if err != nil {
			return nil, err
		}
	} else {
		for _, path := range options.Paths {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			configuredSource, ok := sourceForExplicitPath(configured, absolute)
			if !ok {
				return nil, fmt.Errorf(
					"explicit draw path %s is outside configured source paths", absolute,
				)
			}
			found, err := expand(configuredSource.Agent, configuredSource.Parser, absolute)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, found...)
		}
	}
	since := options.ModifiedSince
	if options.MaxTurns == 0 && since == nil && options.ModifiedUntil == nil && len(options.Paths) == 0 {
		defaultSince := now.Add(-defaultLookback)
		since = &defaultSince
	}
	return filterByModification(unique(inputs), since, options.ModifiedUntil)
}

func (gateway Gateway) Parse(file applicationsource.File) (
	[]domain.FeedstockCandidate,
	[]diagnostic.Warning,
	error,
) {
	logParser, err := parser.For(file.Parser)
	if err != nil {
		return nil, nil, err
	}
	return gateway.parseFile(file, logParser)
}

func (gateway Gateway) parseFile(
	file applicationsource.File,
	logParser parser.Parser,
) ([]domain.FeedstockCandidate, []diagnostic.Warning, error) {
	if gateway.cache != nil {
		return gateway.cache.parse(file, logParser)
	}
	return logParser.Parse(file.Path)
}

func (gateway Gateway) ParseSession(agent, sessionID string) (
	[]domain.FeedstockCandidate,
	[]diagnostic.Warning,
	error,
) {
	files, err := gateway.resolveSession(agent, sessionID)
	if err != nil {
		return nil, nil, err
	}
	sets := make([]applicationsource.CandidateSet, 0, len(files))
	var warnings []diagnostic.Warning
	var firstErr error
	for _, file := range files {
		logParser, parserErr := parser.For(file.Parser)
		if parserErr != nil {
			return nil, warnings, parserErr
		}
		candidates, parsedWarnings, parseErr := gateway.parseFile(file, logParser)
		warnings = append(warnings, parsedWarnings...)
		if parseErr != nil {
			warnings = append(warnings, diagnostic.FromError(file.Path, parseErr))
			if firstErr == nil {
				firstErr = parseErr
			}
			continue
		}
		sets = append(sets, applicationsource.CandidateSet{
			Source: file.Path, Candidates: candidates,
		})
	}
	if len(sets) == 0 {
		return nil, warnings, firstErr
	}
	merged, err := applicationsource.MergeCandidateSets(sets)
	return merged, warnings, err
}

func (gateway Gateway) ReadTurn(agent, sessionID, turnID string) ([]domain.DialogueMessage, error) {
	files, err := gateway.resolveSession(agent, sessionID)
	if err != nil {
		return nil, err
	}
	var found []domain.DialogueMessage
	var firstErr error
	var foundPath string
	for _, file := range files {
		logParser, parserErr := parser.For(file.Parser)
		if parserErr != nil {
			return nil, parserErr
		}
		var messages []domain.DialogueMessage
		var extractErr error
		if gateway.cache == nil {
			messages, extractErr = logParser.ExtractTurn(file.Path, turnID)
		} else {
			var candidates []domain.FeedstockCandidate
			candidates, _, extractErr = gateway.parseFile(file, logParser)
			if extractErr == nil {
				messages, extractErr = dialogueForTurn(candidates, turnID)
			}
		}
		if extractErr != nil {
			if firstErr == nil {
				firstErr = extractErr
			}
			continue
		}
		if found == nil {
			found = messages
			foundPath = file.Path
			continue
		}
		if !slices.Equal(found, messages) {
			return nil, fmt.Errorf(
				"conflicting source turn %s in %s and %s", turnID, foundPath, file.Path,
			)
		}
	}
	if found != nil {
		return found, nil
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("source turn %s was not found in session %s/%s", turnID, agent, sessionID)
}

func (gateway Gateway) ExtractTurn(file applicationsource.File, turnID string) ([]domain.DialogueMessage, error) {
	logParser, err := parser.For(file.Parser)
	if err != nil {
		return nil, err
	}
	if gateway.cache == nil {
		return logParser.ExtractTurn(file.Path, turnID)
	}
	candidates, _, err := gateway.parseFile(file, logParser)
	if err != nil {
		return nil, err
	}
	return dialogueForTurn(candidates, turnID)
}

func dialogueForTurn(
	candidates []domain.FeedstockCandidate,
	turnID string,
) ([]domain.DialogueMessage, error) {
	for _, candidate := range candidates {
		if candidate.TurnID == turnID {
			return append([]domain.DialogueMessage(nil), candidate.Dialogue...), nil
		}
	}
	return nil, fmt.Errorf("source turn %s was not found", turnID)
}

func (Gateway) DiscoverRepository(ctx context.Context, cwd string) string {
	if cwd == "" {
		return ""
	}
	runContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(runContext, "git", "-C", cwd, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func filterByModification(
	inputs []applicationsource.File,
	since,
	until *time.Time,
) ([]applicationsource.File, error) {
	if since == nil && until == nil {
		return inputs, nil
	}
	filtered := make([]applicationsource.File, 0, len(inputs))
	for _, input := range inputs {
		info, err := os.Stat(input.Path)
		if err != nil {
			return nil, err
		}
		modified := info.ModTime()
		if since != nil && modified.Before(*since) {
			continue
		}
		if until != nil && modified.After(*until) {
			continue
		}
		filtered = append(filtered, input)
	}
	return filtered, nil
}

func configuredFiles(
	configured []applicationsource.Configured,
	wanted map[string]struct{},
) ([]applicationsource.File, error) {
	var files []applicationsource.File
	for _, source := range configured {
		if len(wanted) > 0 {
			if _, selected := wanted[source.Agent]; !selected {
				continue
			}
		}
		existing := 0
		for _, path := range source.Paths {
			found, err := expand(source.Agent, source.Parser, path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			existing++
			files = append(files, found...)
		}
		if existing == 0 {
			return nil, fmt.Errorf(
				"configured %s source has no available paths", source.Agent,
			)
		}
	}
	return unique(files), nil
}

func sourceForExplicitPath(
	configured []applicationsource.Configured,
	target string,
) (applicationsource.Configured, bool) {
	for _, source := range configured {
		for _, root := range source.Paths {
			if pathWithin(root, target) {
				return source, true
			}
		}
	}
	return applicationsource.Configured{}, false
}

func pathWithin(root, target string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (gateway Gateway) resolveSession(agent, sessionID string) ([]applicationsource.File, error) {
	catalog, err := gateway.loadCatalog()
	if err != nil {
		return nil, err
	}
	files, exists := catalog[sessionKey(agent, sessionID)]
	if !exists {
		return nil, fmt.Errorf(
			"source session %s/%s was not found in configured paths", agent, sessionID,
		)
	}
	return files, nil
}

func (gateway Gateway) loadCatalog() (map[string][]applicationsource.File, error) {
	if gateway.catalog == nil {
		return buildSessionCatalog(gateway.Configured)
	}
	gateway.catalog.once.Do(func() {
		gateway.catalog.files, gateway.catalog.err = buildSessionCatalog(gateway.Configured)
	})
	return gateway.catalog.files, gateway.catalog.err
}

func buildSessionCatalog(
	configured []applicationsource.Configured,
) (map[string][]applicationsource.File, error) {
	files, err := configuredFiles(configured, nil)
	if err != nil {
		return nil, err
	}
	catalog := make(map[string][]applicationsource.File, len(files))
	for _, file := range files {
		logParser, parserErr := parser.For(file.Parser)
		if parserErr != nil {
			return nil, parserErr
		}
		sessionID, identifyErr := logParser.SessionID(file.Path)
		if identifyErr != nil || strings.TrimSpace(sessionID) == "" {
			sessionID = parser.SessionIDHint(file.Path)
		}
		key := sessionKey(file.Agent, sessionID)
		catalog[key] = append(catalog[key], file)
	}
	return catalog, nil
}

func sessionKey(agent, sessionID string) string {
	return agent + "\x00" + sessionID
}

func expand(agent, parserName, path string) ([]applicationsource.File, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		if agent == "" {
			agent = inferAgent(path)
		}
		if parserName == "" {
			parserName = agent
		}
		return []applicationsource.File{{Agent: agent, Parser: parserName, Path: path}}, nil
	}
	var files []applicationsource.File
	err = filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == "tool-results" || entry.Name() == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(candidate) != ".jsonl" {
			return nil
		}
		candidateAgent := agent
		candidateParser := parserName
		if candidateAgent == "" {
			candidateAgent = inferAgent(candidate)
		}
		if candidateParser == "" {
			candidateParser = candidateAgent
		}
		files = append(files, applicationsource.File{
			Agent: candidateAgent, Parser: candidateParser, Path: candidate,
		})
		return nil
	})
	return files, err
}

func unique(inputs []applicationsource.File) []applicationsource.File {
	seen := make(map[string]struct{}, len(inputs))
	result := make([]applicationsource.File, 0, len(inputs))
	for _, input := range inputs {
		if _, exists := seen[input.Path]; exists {
			continue
		}
		seen[input.Path] = struct{}{}
		result = append(result, input)
	}
	return result
}

func inferAgent(path string) string {
	lower := strings.ToLower(path)
	if strings.Contains(lower, string(filepath.Separator)+".codex"+string(filepath.Separator)) ||
		strings.HasPrefix(filepath.Base(path), "rollout-") {
		return "codex"
	}
	return "claude"
}
