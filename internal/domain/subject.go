package domain

import (
	"net/url"
	"path/filepath"
	"strings"
)

// AliasMatch reports whether a subject master alias identifies a repository
// URL or working directory: exact match, canonical repository identity, or a
// filepath glob pattern.
func AliasMatch(pattern, value string) bool {
	if pattern == "" || value == "" {
		return false
	}
	if pattern == value {
		return true
	}
	if left, right := CanonicalRepo(pattern), CanonicalRepo(value); left != "" && left == right {
		return true
	}
	matched, err := filepath.Match(pattern, value)
	return err == nil && matched
}

// CanonicalRepo reduces a repository URL (including scp-style git addresses)
// to a lowercase host/path identity, or empty when the value is not a URL.
func CanonicalRepo(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if at := strings.Index(value, "@"); at > 0 {
		after := value[at+1:]
		if colon := strings.Index(after, ":"); colon > 0 && !strings.Contains(after[:colon], "/") {
			value = "ssh://" + after[:colon] + "/" + after[colon+1:]
		}
	}
	if !strings.Contains(value, "://") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	path := strings.TrimSuffix(strings.Trim(parsed.Path, "/"), ".git")
	if path == "" {
		return ""
	}
	return strings.ToLower(parsed.Hostname() + "/" + path)
}

// SubjectNameFromSource derives a subject master name from a repository URL
// or path.
func SubjectNameFromSource(source string) string {
	source = strings.TrimSuffix(source, "/")
	base := strings.TrimSuffix(filepath.Base(source), ".git")
	base = strings.ToLower(base)
	var result strings.Builder
	lastDash := false
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			result.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(result.String(), "-")
	if name == "" {
		return "unknown-subject"
	}
	return name
}
