package brew

import (
	"fmt"
	"strings"
)

func loadWritingInstructions(repository Repository, names ...string) (string, error) {
	sections := make([]string, 0, len(names))
	for _, name := range names {
		content, exists, err := repository.ReadWritingGuide(name)
		if err != nil {
			return "", fmt.Errorf("load %s writing guide: %w", name, err)
		}
		content = strings.TrimSpace(content)
		if !exists || content == "" {
			continue
		}
		sections = append(sections, content)
	}
	if len(sections) == 0 {
		return "", nil
	}
	return "Apply the following user-managed writing rules when composing Knowledge prose, unless they conflict with the task requirements below:\n\n" +
		strings.Join(sections, "\n\n"), nil
}
