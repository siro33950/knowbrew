package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/siro33950/knowbrew/internal/adapters/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

var testKnowledgeTypes = []string{
	"definition", "property", "relation", "principle", "constraint", "decision", "preference",
}

func TestDecodeToolArgumentsAcceptsOpenAIStringAndOllamaObject(t *testing.T) {
	for _, raw := range []string{
		`"{\"feedstock_id\":\"feedstock-1\"}"`,
		`{"feedstock_id":"feedstock-1"}`,
	} {
		var values map[string]any
		if err := decodeToolArguments(json.RawMessage(raw), &values); err != nil {
			t.Fatal(err)
		}
		if values["feedstock_id"] != "feedstock-1" {
			t.Fatalf("arguments = %#v", values)
		}
	}
}

func TestCommandForToolMapsAssertionWorkflow(t *testing.T) {
	tests := []struct {
		name      string
		arguments map[string]any
		want      []string
	}{
		{
			name:      "feedstock_context",
			arguments: map[string]any{"feedstock_id": "fs-1"},
			want:      []string{"/bin/knowbrew", "feedstock", "context", "fs-1"},
		},
		{
			name: "knowledge_catalog", arguments: map[string]any{"subject": "knowbrew"},
			want: []string{"/bin/knowbrew", "knowledge", "catalog", "--subject", "knowbrew"},
		},
		{
			name: "knowledge_show", arguments: map[string]any{"knowledge_ids": []any{"kn-1", "kn-2"}},
			want: []string{"/bin/knowbrew", "knowledge", "show", "kn-1", "kn-2"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command, err := commandForTool("/bin/knowbrew", test.name, test.arguments)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(command, test.want) {
				t.Fatalf("command = %#v, want %#v", command, test.want)
			}
		})
	}
}

func TestToolSchemasExposeOnlyReadOperations(t *testing.T) {
	summarize := schemaNames(toolSchemas(TaskSummarize, testKnowledgeTypes))
	if len(summarize) != 0 {
		t.Fatalf("summarize schemas = %#v", summarize)
	}
	annotate := schemaNames(toolSchemas(TaskAnnotate, testKnowledgeTypes))
	if !reflect.DeepEqual(annotate, []string{"feedstock_context"}) {
		t.Fatalf("annotate schemas = %#v", annotate)
	}
	brew := schemaNames(toolSchemas(TaskBrew, testKnowledgeTypes))
	if !reflect.DeepEqual(brew, []string{"knowledge_catalog", "knowledge_show"}) {
		t.Fatalf("brew schemas = %#v", brew)
	}
	for _, forbidden := range []string{"knowledge_propose", "knowledge_search", "feedstock_search", "show"} {
		if containsString(brew, forbidden) {
			t.Fatalf("brew schemas expose %q: %#v", forbidden, brew)
		}
	}

	annotationResult := resultSchema(TaskAnnotate, []string{"observation", "guideline"})
	properties := annotationResult["properties"].(map[string]any)
	items := properties["assertions"].(map[string]any)["items"].(map[string]any)
	itemRequired := items["required"].([]string)
	if !containsString(itemRequired, "subject") {
		t.Fatalf("assertion required = %#v", itemRequired)
	}
	typeProperty := items["properties"].(map[string]any)["type"].(map[string]any)
	if got := typeProperty["enum"]; !reflect.DeepEqual(got, []string{"observation", "guideline"}) {
		t.Fatalf("type enum = %#v", got)
	}
	for _, removed := range []string{"types", "subjects", "speech_acts", "topics", "new_subjects"} {
		if properties[removed] != nil {
			t.Fatalf("annotation schema exposes %q", removed)
		}
	}
	if items["properties"].(map[string]any)["applies_when"] != nil {
		t.Fatal("annotation assertion schema exposes applies_when")
	}

	submit := resultSchema(TaskBrew, testKnowledgeTypes)
	encodedSubmit, err := json.Marshal(submit)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encodedSubmit)
	for _, field := range []string{"verification", "corrected_assertion", "resolution", "knowledge_ids"} {
		if !strings.Contains(text, `"`+field+`"`) {
			t.Fatalf("submit schema missing %q: %s", field, text)
		}
	}
	for _, kind := range []string{"new", "equivalent", "complements", "conflicts"} {
		if !strings.Contains(text, `"`+kind+`"`) {
			t.Fatalf("submit schema missing resolution %q: %s", kind, text)
		}
	}
	for _, forbidden := range []string{"feedstock_id", "assertion_id"} {
		if strings.Contains(text, `"`+forbidden+`"`) {
			t.Fatalf("structured decision exposes %q", forbidden)
		}
	}
}

func TestInvocationEnvironmentReplacesAllInvocationScope(t *testing.T) {
	base := []string{
		"PATH=/bin",
		config.ConfigEnvironment + "=/stale/config.toml",
		config.InvocationFeedstockEnvironment + "=stale-feedstock",
		config.InvocationAssertionEnvironment + "=stale-assertion",
		config.InvocationIDEnvironment + "=stale-invocation",
	}
	environment := invocationEnvironment(
		base, "/current/config.toml", "feedstock-1", "assertion-1", "invocation-1",
	)
	for _, test := range []struct{ key, want string }{
		{config.ConfigEnvironment, "/current/config.toml"},
		{config.InvocationFeedstockEnvironment, "feedstock-1"},
		{config.InvocationAssertionEnvironment, "assertion-1"},
		{config.InvocationIDEnvironment, "invocation-1"},
	} {
		var matches []string
		for _, entry := range environment {
			if strings.HasPrefix(entry, test.key+"=") {
				matches = append(matches, entry)
			}
		}
		if !reflect.DeepEqual(matches, []string{test.key + "=" + test.want}) {
			t.Fatalf("%s entries = %#v", test.key, matches)
		}
	}
}

func TestToolRunnerUsesTaskSpecificAPIModelAndEffort(t *testing.T) {
	var models, efforts []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		models = append(models, body["model"].(string))
		efforts = append(efforts, body["reasoning_effort"].(string))
		return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`), nil
	})
	runner := NewToolRunner(config.Config{LLM: config.LLM{
		Backend: "api", DrawModel: "draw-fast", BrewModel: "brew-quality",
		DrawEffort: "low", BrewEffort: "high",
	}}, "/bin/knowbrew", t.TempDir(), nil)
	runner.Client = &http.Client{Transport: transport}
	for _, task := range []Task{TaskSummarize, TaskAnnotate, TaskBrew} {
		if _, _, err := runner.complete(context.Background(), task, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(models, []string{"draw-fast", "draw-fast", "brew-quality"}) ||
		!reflect.DeepEqual(efforts, []string{"low", "low", "high"}) {
		t.Fatalf("models = %#v, efforts = %#v", models, efforts)
	}
}

func TestToolRunnerOmitsEffortWhenEmptyAndForOllama(t *testing.T) {
	for _, test := range []struct {
		backend, effort, response string
	}{
		{"api", "", `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`},
		{"ollama", "low", `{"message":{"role":"assistant","content":"ok"}}`},
	} {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				return nil, err
			}
			if _, exists := body["reasoning_effort"]; exists {
				t.Fatalf("%s request contains reasoning_effort: %#v", test.backend, body)
			}
			return jsonResponse(test.response), nil
		})
		runner := NewToolRunner(config.Config{LLM: config.LLM{
			Backend: test.backend, DrawModel: "draw", BrewModel: "brew", DrawEffort: test.effort,
		}}, "/bin/knowbrew", t.TempDir(), nil)
		runner.Client = &http.Client{Transport: transport}
		if _, _, err := runner.complete(context.Background(), TaskAnnotate, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolRunnerStreamsCommandOutputOnlyWhenVerbose(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "knowbrew")
	writeExecutable(t, binary, "#!/bin/sh\necho 'tool result marker'\n")
	for _, verbose := range []bool{false, true} {
		rounds := 0
		transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			rounds++
			if rounds == 1 {
				return jsonResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-1","type":"function","function":{"name":"knowledge_catalog","arguments":{"subject":"knowbrew"}}}]}}]}`), nil
			}
			return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"{\"verification\":\"verified\",\"corrected_assertion\":null,\"resolution\":{\"kind\":\"new\",\"knowledge_ids\":[],\"draft\":null}}"}}]}`), nil
		})
		root := t.TempDir()
		var progress bytes.Buffer
		runner := NewToolRunner(config.Config{
			Root: root, Path: filepath.Join(root, "config.toml"),
			LLM: config.LLM{Backend: "api", BrewModel: "brew", Timeout: "5s"},
		}, binary, root, &progress, verbose)
		runner.Client = &http.Client{Transport: transport}
		_, err := runner.RunWithUsage(
			WithAssertion(context.Background(), "assertion-1"),
			TaskBrew, "feedstock-1", "brew",
		)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(progress.String(), "tool result marker"); got != verbose {
			t.Fatalf("verbose=%v progress=%q", verbose, progress.String())
		}
	}
}

func TestToolRunnerReturnsAssertionsWithoutMutationCommand(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "knowbrew")
	capturePath := filepath.Join(t.TempDir(), "commands.txt")
	writeExecutable(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CAPTURE_PATH\"\n")
	t.Setenv("CAPTURE_PATH", capturePath)
	rounds := 0
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		rounds++
		return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"{\"assertions\":[{\"type\":\"property\",\"subject\":\"subject\",\"statement\":\"The value is stable.\"}]}"}}]}`), nil
	})
	runner := NewToolRunner(config.Config{
		Root: root, Path: filepath.Join(root, "config.toml"),
		LLM: config.LLM{Backend: "api", DrawModel: "draw", Timeout: "5s"},
	}, binary, root, nil)
	runner.Client = &http.Client{Transport: transport}
	result, err := runner.Run(context.Background(), TaskAnnotate, "feedstock-1", "classify")
	if err != nil {
		t.Fatal(err)
	}
	if rounds != 1 {
		t.Fatalf("rounds = %d", rounds)
	}
	if string(result.Output) == "" {
		t.Fatal("structured assertions are empty")
	}
	if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
		t.Fatalf("mutation command was executed: %v", err)
	}
}

func TestToolRunnerReturnsSummaryWithoutMutationCommand(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "knowbrew")
	capturePath := filepath.Join(t.TempDir(), "commands.txt")
	writeExecutable(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CAPTURE_PATH\"\n")
	t.Setenv("CAPTURE_PATH", capturePath)
	rounds := 0
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		rounds++
		return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"{\"summary\":\"summary\"}"}}]}`), nil
	})
	runner := NewToolRunner(config.Config{
		Root: root, Path: filepath.Join(root, "config.toml"),
		LLM: config.LLM{Backend: "api", DrawModel: "draw", Timeout: "5s"},
	}, binary, root, nil)
	runner.Client = &http.Client{Transport: transport}
	result, err := runner.Run(context.Background(), TaskSummarize, "feedstock-1", "summarize")
	if err != nil {
		t.Fatal(err)
	}
	if rounds != 1 {
		t.Fatalf("rounds = %d", rounds)
	}
	if string(result.Output) != `{"summary":"summary"}` {
		t.Fatalf("result = %s", result.Output)
	}
	if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
		t.Fatalf("mutation command was executed: %v", err)
	}
}

func TestToolRunnerCanLoadContextOnceBeforeAnnotating(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(t.TempDir(), "knowbrew")
	capturePath := filepath.Join(t.TempDir(), "commands.txt")
	writeExecutable(t, binary, "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CAPTURE_PATH\"\nif [ \"$2\" = context ]; then printf '%s\\n' '{\"target_offset\":0,\"turns\":[]}'; fi\n")
	t.Setenv("CAPTURE_PATH", capturePath)
	rounds := 0
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		rounds++
		if rounds == 1 {
			return jsonResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-1","type":"function","function":{"name":"feedstock_context","arguments":{"feedstock_id":"feedstock-1"}}}]}}]}`), nil
		}
		return jsonResponse(`{"choices":[{"message":{"role":"assistant","content":"{\"assertions\":[]}"}}]}`), nil
	})
	runner := NewToolRunner(config.Config{
		Root: root, Path: filepath.Join(root, "config.toml"),
		LLM: config.LLM{Backend: "api", DrawModel: "draw", Timeout: "5s"},
	}, binary, root, nil)
	runner.Client = &http.Client{Transport: transport}
	if _, err := runner.RunWithUsage(context.Background(), TaskAnnotate, "feedstock-1", "classify"); err != nil {
		t.Fatal(err)
	}
	if rounds != 2 {
		t.Fatalf("rounds = %d, want 2", rounds)
	}
	data, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	commands := string(data)
	if !strings.Contains(commands, "feedstock context feedstock-1") ||
		strings.Contains(commands, "feedstock annotate") {
		t.Fatalf("commands = %q", commands)
	}
}

func TestToolRunnerRejectsReadToolForAnnotate(t *testing.T) {
	transport := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return jsonResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"read-1","type":"function","function":{"name":"knowledge_show","arguments":{"knowledge_ids":["kn-1"]}}}]}}]}`), nil
	})
	root := t.TempDir()
	runner := NewToolRunner(config.Config{
		Root: root, Path: filepath.Join(root, "config.toml"),
		LLM: config.LLM{Backend: "api", DrawModel: "draw", Timeout: "5s"},
	}, "/bin/knowbrew", root, nil)
	runner.Client = &http.Client{Transport: transport}
	_, err := runner.Run(context.Background(), TaskAnnotate, "feedstock-1", "classify")
	if err == nil || !strings.Contains(err.Error(), `tool "knowledge_show" is not allowed for annotate`) {
		t.Fatalf("error = %v", err)
	}
}

func schemaNames(definitions []map[string]any) []string {
	result := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		function := definition["function"].(map[string]any)
		result = append(result, function["name"].(string))
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
