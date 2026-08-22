package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const SchemaVersion = 8

type Status string

type KnowledgeType string

const (
	StatusPending     Status = "pending"
	StatusActive      Status = "active"
	StatusInvalidated Status = "invalidated"
	StatusSuperseded  Status = "superseded"
)

type SessionRef struct {
	ID string `yaml:"id" json:"id"`
}

type DialogueMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Feedstock struct {
	Schema      int             `yaml:"schema" json:"schema"`
	ID          string          `yaml:"id" json:"id"`
	TurnID      string          `yaml:"turn_id" json:"turn_id"`
	Session     SessionRef      `yaml:"session" json:"session"`
	Timestamp   time.Time       `yaml:"timestamp" json:"timestamp"`
	Agent       string          `yaml:"agent" json:"agent"`
	CWD         string          `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Repo        string          `yaml:"repo,omitempty" json:"repo,omitempty"`
	Branch      string          `yaml:"branch,omitempty" json:"branch,omitempty"`
	Types       []KnowledgeType `yaml:"types" json:"types"`
	Summary     string          `yaml:"summary" json:"summary"`
	DraftedAt   *time.Time      `yaml:"drafted_at,omitempty" json:"drafted_at,omitempty"`
	ExtractedAt *time.Time      `yaml:"extracted_at,omitempty" json:"extracted_at,omitempty"`
}

type Knowledge struct {
	ID            string        `yaml:"id" json:"id"`
	Created       time.Time     `yaml:"created" json:"created"`
	Updated       time.Time     `yaml:"updated" json:"updated"`
	EstablishedBy string        `yaml:"established_by,omitempty" json:"established_by,omitempty"`
	Type          KnowledgeType `yaml:"type" json:"type"`
	Subject       string        `yaml:"subject,omitempty" json:"subject,omitempty"`
	Feedstocks    []string      `yaml:"feedstocks" json:"feedstocks"`
	Approved      bool          `yaml:"approved" json:"approved"`
	Supersedes    []string      `yaml:"supersedes,omitempty" json:"supersedes,omitempty"`
	SupersededBy  string        `yaml:"superseded_by,omitempty" json:"superseded_by,omitempty"`
	SupersededAt  *time.Time    `yaml:"superseded_at,omitempty" json:"superseded_at,omitempty"`
	InvalidatedAt *time.Time    `yaml:"invalidated_at,omitempty" json:"invalidated_at,omitempty"`
	OrganizedAt   *time.Time    `yaml:"organized_at,omitempty" json:"organized_at,omitempty"`
	Status        Status        `yaml:"-" json:"status"`
}

type MasterEntry struct {
	Name       string   `json:"name"`
	Definition string   `json:"definition,omitempty"`
	Example    string   `json:"example,omitempty"`
	Includes   []string `json:"includes,omitempty"`
	Excludes   []string `json:"excludes,omitempty"`
	Aliases    []string `json:"aliases,omitempty"`
	Documents  []string `json:"documents,omitempty"`
}

type SemanticSubject struct {
	Name       string   `json:"name"`
	Definition string   `json:"definition,omitempty"`
	Includes   []string `json:"includes,omitempty"`
	Excludes   []string `json:"excludes,omitempty"`
}

type FeedstockCandidate struct {
	ID                   string            `json:"id"`
	TurnID               string            `json:"turn_id"`
	Session              SessionRef        `json:"session"`
	Timestamp            time.Time         `json:"timestamp"`
	Agent                string            `json:"agent"`
	CWD                  string            `json:"cwd,omitempty"`
	Repo                 string            `json:"repo,omitempty"`
	Branch               string            `json:"branch,omitempty"`
	Dialogue             []DialogueMessage `json:"-"`
	SourceSequence       int64             `json:"-"`
	SourceOwnerSessionID string            `json:"-"`
}

var (
	knowledgeIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	identifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,254}$`)
)

func ValidateStatus(status Status) error {
	switch status {
	case StatusPending, StatusActive, StatusInvalidated, StatusSuperseded:
		return nil
	default:
		return fmt.Errorf("invalid status %q", status)
	}
}

func EffectiveKnowledgeStatus(knowledge Knowledge) Status {
	if strings.TrimSpace(knowledge.SupersededBy) != "" {
		return StatusSuperseded
	}
	if knowledge.InvalidatedAt != nil {
		return StatusInvalidated
	}
	if knowledge.Approved {
		return StatusActive
	}
	return StatusPending
}

func ValidateKnowledgeTypeName(value KnowledgeType) error {
	return ValidateIdentifier(strings.TrimSpace(string(value)), "knowledge type")
}

func NormalizeKnowledgeTypes(values []KnowledgeType) ([]KnowledgeType, error) {
	out := make([]KnowledgeType, 0, len(values))
	seen := make(map[KnowledgeType]struct{}, len(values))
	for _, value := range values {
		value = KnowledgeType(strings.TrimSpace(string(value)))
		if err := ValidateKnowledgeTypeName(value); err != nil {
			return nil, err
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	slices.Sort(out)
	return out, nil
}

func ParseKnowledgeTypes(values []string) ([]KnowledgeType, error) {
	parsed := make([]KnowledgeType, len(values))
	for index, value := range values {
		parsed[index] = KnowledgeType(value)
	}
	return NormalizeKnowledgeTypes(parsed)
}

func ValidateKnowledgeID(id string) error {
	if !knowledgeIDPattern.MatchString(id) {
		return fmt.Errorf("knowledge ID %q must be lowercase kebab-case", id)
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
	if err := ValidateIdentifier(feedstock.TurnID, "source turn ID"); err != nil {
		return err
	}
	if feedstock.Session.ID == "" {
		return errors.New("feedstock session ID is required")
	}
	if feedstock.Timestamp.IsZero() {
		return errors.New("feedstock timestamp is required")
	}
	if feedstock.Agent != "claude" && feedstock.Agent != "codex" {
		return fmt.Errorf("unsupported agent %q", feedstock.Agent)
	}
	if feedstock.DraftedAt == nil {
		if feedstock.ExtractedAt != nil {
			return errors.New("undrafted feedstock must not have extracted_at")
		}
		return nil
	}
	if feedstock.ExtractedAt != nil && feedstock.ExtractedAt.IsZero() {
		return errors.New("feedstock extracted_at must not be zero")
	}
	if strings.TrimSpace(feedstock.Summary) == "" {
		return errors.New("drafted feedstock summary is required")
	}
	types, err := NormalizeKnowledgeTypes(feedstock.Types)
	if err != nil {
		return fmt.Errorf("feedstock types: %w", err)
	}
	if !slices.Equal(types, feedstock.Types) {
		return errors.New("feedstock types must be unique and sorted")
	}
	return nil
}

func ValidateKnowledge(knowledge Knowledge) error {
	if err := ValidateKnowledgeID(knowledge.ID); err != nil {
		return err
	}
	if knowledge.Created.IsZero() || knowledge.Updated.IsZero() {
		return errors.New("knowledge created and updated timestamps are required")
	}
	if err := ValidateKnowledgeTypeName(knowledge.Type); err != nil {
		return fmt.Errorf("knowledge type: %w", err)
	}
	if len(knowledge.Feedstocks) == 0 {
		return errors.New("knowledge feedstocks must not be empty")
	}
	if knowledge.EstablishedBy != "" {
		establishedBy := MasterName(knowledge.EstablishedBy)
		if err := ValidateIdentifier(establishedBy, "established_by feedstock ID"); err != nil {
			return err
		}
		if !slices.Contains(NormalizeMasterNames(knowledge.Feedstocks), establishedBy) {
			return errors.New("knowledge established_by must also appear in feedstocks")
		}
	}
	subject := MasterName(knowledge.Subject)
	if subject != "" {
		if err := ValidateIdentifier(subject, "knowledge subject"); err != nil {
			return err
		}
	}
	if knowledge.OrganizedAt == nil {
		if knowledge.Approved {
			return errors.New("unorganized knowledge must not be approved")
		}
		if len(knowledge.Supersedes) != 0 || strings.TrimSpace(knowledge.SupersededBy) != "" ||
			knowledge.SupersededAt != nil || knowledge.InvalidatedAt != nil {
			return errors.New("unorganized knowledge must not participate in the lifecycle")
		}
	} else {
		if knowledge.OrganizedAt.IsZero() {
			return errors.New("knowledge organized_at must not be zero")
		}
		if subject == "" {
			return errors.New("organized knowledge subject is required")
		}
	}
	if knowledge.InvalidatedAt != nil && strings.TrimSpace(knowledge.SupersededBy) != "" {
		return errors.New("knowledge cannot be both invalidated and superseded")
	}
	if (knowledge.SupersededAt == nil) != (strings.TrimSpace(knowledge.SupersededBy) == "") {
		return errors.New("superseded knowledge requires both superseded_by and superseded_at")
	}
	for _, id := range knowledge.Supersedes {
		if err := ValidateKnowledgeID(MasterName(id)); err != nil {
			return fmt.Errorf("invalid supersedes entry: %w", err)
		}
	}
	if knowledge.SupersededBy != "" {
		if err := ValidateKnowledgeID(MasterName(knowledge.SupersededBy)); err != nil {
			return fmt.Errorf("invalid superseded_by: %w", err)
		}
	}
	return nil
}

func ValidateMaster(entry MasterEntry) error {
	if err := ValidateIdentifier(entry.Name, "master name"); err != nil {
		return err
	}
	if strings.ContainsAny(entry.Definition, "\r\n") {
		return errors.New("master definition must be one line")
	}
	if strings.ContainsAny(entry.Example, "\r\n") {
		return errors.New("master example must be one line")
	}
	for _, value := range append(append([]string(nil), entry.Includes...), entry.Excludes...) {
		if strings.TrimSpace(value) == "" {
			return errors.New("master scope entries must not be empty")
		}
		if strings.ContainsAny(value, "\r\n") {
			return errors.New("master scope entries must be one line")
		}
	}
	for _, value := range entry.Documents {
		if err := ValidateIdentifier(MasterName(value), "template name"); err != nil {
			return err
		}
	}
	return nil
}

func ValidateTypeMaster(entry MasterEntry) error {
	if err := ValidateMaster(entry); err != nil {
		return err
	}
	if strings.TrimSpace(entry.Definition) == "" {
		return errors.New("type master definition is required")
	}
	return nil
}

func SemanticSubjects(entries []MasterEntry) []SemanticSubject {
	result := make([]SemanticSubject, 0, len(entries))
	for _, entry := range entries {
		result = append(result, SemanticSubject{
			Name: entry.Name, Definition: entry.Definition,
			Includes: append([]string(nil), entry.Includes...),
			Excludes: append([]string(nil), entry.Excludes...),
		})
	}
	return result
}

func MasterName(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[[") && strings.HasSuffix(value, "]]") {
		value = strings.TrimSpace(value[2 : len(value)-2])
	}
	return value
}

func NormalizeMasterNames(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		normalized = append(normalized, MasterName(value))
	}
	return UniqueSorted(normalized)
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
