package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const MaxJSONLRecordBytes = 64 << 20

type scanPosition struct {
	Offset       int64
	Line         int
	SnapshotSize int64
}

func scanSnapshot(path string, visit func(int, []byte) (bool, error)) error {
	return scanSnapshotWithLimit(path, MaxJSONLRecordBytes, visit)
}

func scanSnapshotWithLimit(
	path string,
	limit int,
	visit func(int, []byte) (bool, error),
) error {
	_, err := scanSnapshotFrom(path, scanPosition{}, limit, visit)
	return err
}

func scanSnapshotFrom(
	path string,
	start scanPosition,
	limit int,
	visit func(int, []byte) (bool, error),
) (scanPosition, error) {
	file, err := os.Open(path)
	if err != nil {
		return start, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return start, err
	}
	if start.Offset < 0 || start.Offset > info.Size() {
		return start, fmt.Errorf("invalid JSONL offset %d for %d-byte file", start.Offset, info.Size())
	}
	start.SnapshotSize = info.Size()
	reader := bufio.NewReader(io.NewSectionReader(file, start.Offset, info.Size()-start.Offset))
	position := start
	for {
		line, consumed, readErr := readLimitedRecord(reader, limit)
		if readErr == io.EOF {
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(bytes.TrimSpace(line)) == 0 || !json.Valid(line) {
				return position, nil
			}
			position.Line++
			_, err := visit(position.Line, line)
			if err != nil {
				return start, fmt.Errorf("line %d: %w", position.Line, err)
			}
			position.Offset += consumed
			return position, nil
		}
		if readErr != nil {
			return start, fmt.Errorf("line %d: %w", position.Line+1, readErr)
		}
		position.Line++
		line = bytes.TrimSuffix(line, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if len(bytes.TrimSpace(line)) == 0 {
			position.Offset += consumed
			continue
		}
		more, err := visit(position.Line, line)
		if err != nil {
			return start, fmt.Errorf("line %d: %w", position.Line, err)
		}
		position.Offset += consumed
		if !more {
			return position, nil
		}
	}
}

func readLimitedRecord(reader *bufio.Reader, limit int) ([]byte, int64, error) {
	if limit < 1 {
		return nil, 0, errors.New("JSONL record limit must be positive")
	}
	line := make([]byte, 0, min(limit, 64<<10))
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit+2 {
			return nil, 0, fmt.Errorf("JSONL record exceeds %d bytes", limit)
		}
		line = append(line, fragment...)
		switch {
		case err == nil:
			consumed := int64(len(line))
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) > limit {
				return nil, 0, fmt.Errorf("JSONL record exceeds %d bytes", limit)
			}
			return line, consumed, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			consumed := int64(len(line))
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) > limit {
				return nil, 0, fmt.Errorf("JSONL record exceeds %d bytes", limit)
			}
			return line, consumed, io.EOF
		default:
			return nil, 0, err
		}
	}
}
