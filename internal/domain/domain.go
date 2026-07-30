package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const SchemaVersion = 1

type Status string

const (
	StatusPending     Status = "pending"
	StatusActive      Status = "active"
	StatusInvalidated Status = "invalidated"
)

type SessionRef struct {
	ID   string `yaml:"id" json:"id"`
	Path string `yaml:"path" json:"path"`
}

type Command struct {
	Command  string `yaml:"command" json:"command"`
	ExitCode *int   `yaml:"exit_code,omitempty" json:"exit_code,omitempty"`
}

type Feedstock struct {
	Schema       int        `yaml:"schema" json:"schema"`
	ID           string     `yaml:"id" json:"id"`
	Session      SessionRef `yaml:"session" json:"session"`
	Timestamp    time.Time  `yaml:"timestamp" json:"timestamp"`
	Agent        string     `yaml:"agent" json:"agent"`
	CWD          string     `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Repo         string     `yaml:"repo,omitempty" json:"repo,omitempty"`
	Branch       string     `yaml:"branch,omitempty" json:"branch,omitempty"`
	Commands     []Command  `yaml:"commands,omitempty" json:"commands,omitempty"`
	FilesChanged []string   `yaml:"files_changed,omitempty" json:"files_changed,omitempty"`
	Errors       []string   `yaml:"errors,omitempty" json:"errors,omitempty"`
	UserQuote    string     `yaml:"user_quote" json:"user_quote"`
	SpeechActs   []string   `yaml:"speech_acts" json:"speech_acts"`
	Topics       []string   `yaml:"topics" json:"topics"`
	Subjects     []string   `yaml:"subjects" json:"subjects"`
	Summary      string     `yaml:"summary" json:"summary"`
	BrewedAt     *time.Time `yaml:"brewed_at,omitempty" json:"brewed_at,omitempty"`
}

type Knowledge struct {
	Created       time.Time  `yaml:"created" json:"created"`
	Updated       time.Time  `yaml:"updated" json:"updated"`
	Project       string     `yaml:"project,omitempty" json:"project,omitempty"`
	Topics        []string   `yaml:"topics" json:"topics"`
	AppliesWhen   string     `yaml:"applies_when" json:"applies_when"`
	Sources       []string   `yaml:"sources" json:"sources"`
	Status        Status     `yaml:"status" json:"status"`
	InvalidatedAt *time.Time `yaml:"invalidated_at,omitempty" json:"invalidated_at,omitempty"`
	Trigger       string     `yaml:"trigger,omitempty" json:"trigger,omitempty"`
}

type MasterEntry struct {
	Name       string    `yaml:"name" json:"name"`
	Definition string    `yaml:"definition" json:"definition"`
	Aliases    []string  `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Status     Status    `yaml:"status" json:"status"`
	Created    time.Time `yaml:"created" json:"created"`
	Updated    time.Time `yaml:"updated" json:"updated"`
}

type FeedstockCandidate struct {
	ID           string     `json:"id"`
	Session      SessionRef `json:"session"`
	Timestamp    time.Time  `json:"timestamp"`
	Agent        string     `json:"agent"`
	CWD          string     `json:"cwd,omitempty"`
	Repo         string     `json:"repo,omitempty"`
	Branch       string     `json:"branch,omitempty"`
	Commands     []Command  `json:"commands,omitempty"`
	FilesChanged []string   `json:"files_changed,omitempty"`
	Errors       []string   `json:"errors,omitempty"`
	UserQuote    string     `json:"user_quote"`
	Subjects     []string   `json:"subjects,omitempty"`
}

var (
	slugPattern       = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
)

func ValidateStatus(status Status) error {
	switch status {
	case StatusPending, StatusActive, StatusInvalidated:
		return nil
	default:
		return fmt.Errorf("invalid status %q", status)
	}
}

func ValidateSlug(slug string) error {
	if !slugPattern.MatchString(slug) {
		return fmt.Errorf("slug %q must be lowercase kebab-case", slug)
	}
	return nil
}

func ValidateIdentifier(value, field string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s %q contains unsupported characters", field, value)
	}
	return nil
}

func ValidateFeedstock(feedstock Feedstock) error {
	if feedstock.Schema != SchemaVersion {
		return fmt.Errorf("unsupported feedstock schema %d", feedstock.Schema)
	}
	if err := ValidateIdentifier(feedstock.ID, "feedstock ID"); err != nil {
		return err
	}
	if feedstock.Session.ID == "" || feedstock.Session.Path == "" {
		return errors.New("feedstock session ID and path are required")
	}
	if feedstock.Timestamp.IsZero() {
		return errors.New("feedstock timestamp is required")
	}
	if feedstock.Agent != "claude" && feedstock.Agent != "codex" {
		return fmt.Errorf("unsupported agent %q", feedstock.Agent)
	}
	if strings.TrimSpace(feedstock.UserQuote) == "" {
		return errors.New("feedstock user_quote is required")
	}
	if strings.TrimSpace(feedstock.Summary) == "" {
		return errors.New("feedstock summary is required")
	}
	if len(feedstock.SpeechActs) == 0 {
		return errors.New("feedstock speech_acts must not be empty")
	}
	if len(feedstock.Subjects) == 0 {
		return errors.New("feedstock subjects must not be empty")
	}
	return nil
}

func ValidateKnowledge(knowledge Knowledge) error {
	if knowledge.Created.IsZero() || knowledge.Updated.IsZero() {
		return errors.New("knowledge created and updated timestamps are required")
	}
	if strings.TrimSpace(knowledge.AppliesWhen) == "" {
		return errors.New("knowledge applies_when is required")
	}
	if strings.ContainsAny(knowledge.AppliesWhen, "\r\n") {
		return errors.New("knowledge applies_when must be one line")
	}
	if len(knowledge.Sources) == 0 {
		return errors.New("knowledge sources must not be empty")
	}
	if err := ValidateStatus(knowledge.Status); err != nil {
		return err
	}
	if knowledge.Status == StatusInvalidated && knowledge.InvalidatedAt == nil {
		return errors.New("invalidated knowledge requires invalidated_at")
	}
	if knowledge.Status != StatusInvalidated && knowledge.InvalidatedAt != nil {
		return errors.New("invalidated_at is only valid for invalidated knowledge")
	}
	return nil
}

func ValidateMaster(entry MasterEntry) error {
	if err := ValidateIdentifier(entry.Name, "master name"); err != nil {
		return err
	}
	if strings.TrimSpace(entry.Definition) == "" {
		return errors.New("master definition is required")
	}
	if strings.ContainsAny(entry.Definition, "\r\n") {
		return errors.New("master definition must be one line")
	}
	if err := ValidateStatus(entry.Status); err != nil {
		return err
	}
	if entry.Created.IsZero() || entry.Updated.IsZero() {
		return errors.New("master created and updated timestamps are required")
	}
	return nil
}

func UniqueSorted(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out
}
