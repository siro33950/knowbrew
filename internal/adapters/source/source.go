package source

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/siro33950/knowbrew/internal/adapters/source/parser"
	"github.com/siro33950/knowbrew/internal/application/diagnostic"
	applicationsource "github.com/siro33950/knowbrew/internal/application/source"
	"github.com/siro33950/knowbrew/internal/domain"
)

const defaultLookback = 24 * time.Hour

type Gateway struct{}

func (Gateway) Collect(
	configured []applicationsource.Configured,
	options applicationsource.Selection,
	now time.Time,
) ([]applicationsource.File, error) {
	if options.All && len(options.Paths) > 0 {
		return nil, errors.New("--all cannot be used with explicit paths")
	}
	if len(options.Sources) > 0 && len(options.Paths) > 0 {
		return nil, errors.New("--source cannot be used with explicit paths")
	}
	if options.All && (options.ModifiedSince != nil || options.ModifiedUntil != nil) {
		return nil, errors.New("--all cannot be used with --since or --until")
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
		for _, configuredSource := range configured {
			if len(wantedSources) > 0 {
				if _, wanted := wantedSources[configuredSource.Agent]; !wanted {
					continue
				}
			}
			found, err := expand(configuredSource.Agent, configuredSource.Parser, configuredSource.Path)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, found...)
		}
	} else {
		for _, path := range options.Paths {
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil, err
			}
			found, err := expand("", "", absolute)
			if err != nil {
				return nil, err
			}
			inputs = append(inputs, found...)
		}
	}
	since := options.ModifiedSince
	if !options.All && since == nil && options.ModifiedUntil == nil && len(options.Paths) == 0 {
		defaultSince := now.Add(-defaultLookback)
		since = &defaultSince
	}
	return filterByModification(unique(inputs), since, options.ModifiedUntil)
}

func (Gateway) Parse(file applicationsource.File) (
	[]domain.FeedstockCandidate,
	[]diagnostic.Warning,
	error,
) {
	logParser, err := parser.For(file.Parser)
	if err != nil {
		return nil, nil, err
	}
	return logParser.Parse(file.Path)
}

func (Gateway) ParseSession(agent, path string) (
	[]domain.FeedstockCandidate,
	[]diagnostic.Warning,
	error,
) {
	logParser, err := parser.For(agent)
	if err != nil {
		return nil, nil, err
	}
	return logParser.Parse(path)
}

func (Gateway) ExtractTurn(file applicationsource.File, turnID string) ([]domain.DialogueMessage, error) {
	logParser, err := parser.For(file.Parser)
	if err != nil {
		return nil, err
	}
	return logParser.ExtractTurn(file.Path, turnID)
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
			if entry.Name() == "tool-results" {
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
