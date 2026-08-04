package runstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/siro33950/knowbrew/internal/adapters/fsutil"
	"github.com/siro33950/knowbrew/internal/application/distill"
)

type DistillCursor struct {
	Path string
}

func (cursor DistillCursor) Load() (distill.CursorPosition, bool, error) {
	data, err := os.ReadFile(cursor.Path)
	if errors.Is(err, os.ErrNotExist) {
		return distill.CursorPosition{}, false, nil
	}
	if err != nil {
		return distill.CursorPosition{}, false, fmt.Errorf("read distill cursor: %w", err)
	}
	var position distill.CursorPosition
	if err := json.Unmarshal(data, &position); err != nil {
		return distill.CursorPosition{}, false, fmt.Errorf("decode distill cursor: %w", err)
	}
	return position, true, nil
}

func (cursor DistillCursor) Save(position distill.CursorPosition) error {
	data, err := json.MarshalIndent(position, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fsutil.AtomicWrite(cursor.Path, data, 0o600); err != nil {
		return fmt.Errorf("write distill cursor: %w", err)
	}
	return nil
}
