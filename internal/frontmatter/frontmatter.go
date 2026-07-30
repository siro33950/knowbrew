package frontmatter

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

const delimiter = "---"

func Encode(header any, body string) ([]byte, error) {
	var yamlData bytes.Buffer
	encoder := yaml.NewEncoder(&yamlData)
	encoder.SetIndent(2)
	if err := encoder.Encode(header); err != nil {
		return nil, fmt.Errorf("encode frontmatter: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("close frontmatter encoder: %w", err)
	}
	var output bytes.Buffer
	output.WriteString(delimiter + "\n")
	output.Write(yamlData.Bytes())
	output.WriteString(delimiter + "\n")
	if body != "" {
		output.WriteString("\n")
		output.WriteString(strings.TrimRight(body, "\n"))
		output.WriteByte('\n')
	}
	return output.Bytes(), nil
}

func Decode(data []byte, header any) (string, error) {
	if !bytes.HasPrefix(data, []byte(delimiter+"\n")) {
		return "", errors.New("missing YAML frontmatter")
	}
	rest := data[len(delimiter)+1:]
	end := bytes.Index(rest, []byte("\n"+delimiter+"\n"))
	if end < 0 {
		return "", errors.New("unterminated YAML frontmatter")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(rest[:end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(header); err != nil {
		return "", fmt.Errorf("decode frontmatter: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("decode extra frontmatter document: %w", err)
	}
	body := rest[end+len("\n"+delimiter+"\n"):]
	body = bytes.TrimPrefix(body, []byte("\n"))
	return string(body), nil
}
