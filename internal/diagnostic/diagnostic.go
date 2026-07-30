package diagnostic

import (
	"fmt"
	"io"
)

type Warning struct {
	Message string `json:"message"`
	Path    string `json:"path"`
	Reason  string `json:"reason"`
}

func FromError(path string, err error) Warning {
	reason := err.Error()
	return Warning{
		Message: fmt.Sprintf("skipped: %s: %s", path, reason),
		Path:    path,
		Reason:  reason,
	}
}

func (warning Warning) String() string {
	if warning.Message != "" {
		return warning.Message
	}
	return fmt.Sprintf("skipped: %s: %s", warning.Path, warning.Reason)
}

func Add(target *[]Warning, writer io.Writer, warnings ...Warning) {
	seen := make(map[string]struct{}, len(*target))
	for _, warning := range *target {
		seen[warning.Path+"\x00"+warning.Reason] = struct{}{}
	}
	for _, warning := range warnings {
		key := warning.Path + "\x00" + warning.Reason
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		*target = append(*target, warning)
		if writer != nil {
			fmt.Fprintln(writer, warning.String())
		}
	}
}
