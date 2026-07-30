# knowbrew

`knowbrew` is a local-first CLI that brews durable knowledge from Claude Code
and Codex session logs.

It keeps three distinct layers:

1. Original agent logs remain the immutable source of truth.
2. `knowbrew draw` creates immutable feedstock records, one per user turn, with
   the user's original message, machine metadata, and constrained LLM
   classification.
3. `knowbrew brew` creates one-claim knowledge records as `status: pending`.
   Only a human can promote knowledge to `status: active`.

Retrieval always returns JSON so untrusted remembered content remains
structurally separated from agent instructions.

[日本語版 README](README.ja.md)

## Requirements

- Go 1.25 or later when building from source
- One configured LLM backend:
  - Claude Code CLI
  - Codex CLI
  - an OpenAI-compatible Chat Completions endpoint with tool calling
  - Ollama with a tool-capable model

SQLite FTS5 is provided through a pure-Go driver; CGO is not required.

## Installation

The recommended installation for Claude Code and Codex users does not require a
Go toolchain:

```sh
npm install -g knowbrew
```

The npm launcher runs the packaged native binary. `knowbrew init` therefore
records that binary's absolute path in generated hooks; the global npm package
path is version-independent, so normal package updates keep the hook valid.

With Go 1.25 or later:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

Prebuilt archives and checksums are also available from
[GitHub Releases](https://github.com/siro33950/knowbrew/releases).

To build a checkout:

```sh
go build -o knowbrew ./cmd/knowbrew
```

## Initialize

Change to the directory that should own the knowledge files and run:

```sh
knowbrew init
```

`init` is intentionally interactive. It selects detected log sources and an
LLM backend, then asks before merging SessionStart hooks and static usage
instructions into Claude Code or Codex. Existing settings and instruction
content are retained.

The configured root contains:

```text
<root>/
  .knowbrew/config.toml
  feedstocks/
  knowledge/
  masters/
    topics/
    subjects/
  .state/
```

`~/.config/knowbrew/location.toml` points to the root-local configuration, so
the CLI works from any current directory. `KNOWBREW_CONFIG` can override the
locator for tests or temporary invocations.

## Commands

```text
knowbrew init
knowbrew draw [path...]
knowbrew brew
knowbrew knowledge [keywords...]
knowbrew feedstock [keywords...]
knowbrew show <feedstock-id...>
knowbrew feedstock annotate ...
knowbrew knowledge create ...
knowbrew knowledge add-source ...
knowbrew knowledge invalidate ...
```

`kn` is an alias for `knowledge`.

`feedstock annotate` and the three knowledge subcommands are validated mutation
boundaries used by LLM backends. The CLI validates schemas, statuses,
vocabulary, source references, and paths before every atomic write.

### Draw session logs

With no arguments, `draw` scans sources selected during initialization:

```sh
knowbrew draw
```

Relocated or backed-up logs can be supplied explicitly:

```sh
knowbrew draw /backup/claude-session.jsonl /backup/codex-sessions/
```

Import state is keyed by agent and session ID rather than path. Moving a log
does not create duplicate feedstocks. The state file is only a cache; stable
feedstock IDs make it reconstructable from the Markdown records.

### Brew knowledge

```sh
knowbrew brew
```

For every unprocessed feedstock, the backend receives only its ID. It reads the
candidate with `show`, finds comparison context with `knowledge` and
`feedstock`, and chooses one outcome:

- create pending knowledge;
- add the candidate as a source of existing knowledge;
- invalidate contradicted knowledge;
- do nothing.

New knowledge is always `pending`. To expose it to normal retrieval, review it
and manually change its frontmatter:

```yaml
status: active
```

There is no automatic pending-to-active transition.

### Search knowledge

```sh
knowbrew knowledge -- sqlite locking
knowbrew knowledge --subject knowbrew --topic testing -- sqlite
knowbrew knowledge --include-pending -- similar claim
knowbrew kn -- create
```

The default includes only active knowledge. `--include-pending` also exposes
pending records for inspection and brewing; invalidated knowledge is never
returned.

SessionStart integrations call:

```sh
knowbrew knowledge --trigger always
```

This returns an `approved_rules` object containing only human-approved active
knowledge whose trigger is `always`. `--trigger always` cannot be combined with
`--include-pending`.

### Search feedstock and inspect provenance

```sh
knowbrew feedstock -- sqlite locking
knowbrew feedstock --subject knowbrew --topic testing
knowbrew feedstock --session <session-id> --agent claude
knowbrew feedstock --last 10
knowbrew show claude-01234567-89ab-cdef-0123-456789abcdef-t000001
```

`--last N` selects the latest N feedstocks and returns them oldest to newest;
it cannot be combined with keywords.

Both search commands accept `--subject`, `--topic`, `--since`, `--until`,
`--limit` (default 20), `--max-tokens` (default 2000), and `--reindex`.
Keywords use FTS5 relevance ordering; no keywords means newest first. Always
place keywords after `--`. This disambiguates keywords such as `create` and
`annotate` from mutation subcommands.

FTS5 relevance ordering applies when every search term is at least three
Unicode code points long. If any term is shorter, search falls back to
substring matching in newest-first order and reports a score of 0.

Knowledge hits contain a slug, claim, `applies_when`, and Markdown path.
Feedstock hits contain an ID, timestamp, summary, subjects, and topics.
Keyword hits include a score. Responses also report total and returned counts
and whether token or result limits truncated the output.

## LLM backends

The root-local configuration contains:

```toml
root = ".."

[llm]
backend = "claude-cli"
model = ""
timeout = "5m"

[[sources]]
agent = "claude"
path = "/Users/example/.claude/projects"
parser = "claude"
```

Supported backends are `claude-cli`, `codex-cli`, `api`, and `ollama`.
`timeout` accepts Go duration syntax such as `30s`, `5m`, or `1h` and defaults
to five minutes.

For `api`, configure:

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

`KNOWBREW_API_URL` defaults to OpenAI's Chat Completions endpoint. API keys are
read only from the environment and are never written to the knowledge root.
For `ollama`, `OLLAMA_HOST` defaults to `http://127.0.0.1:11434`.

CLI backends launch their corresponding non-interactive agent. API and Ollama
backends expose the same constrained read and write operations as tool calls.
Ordinary model prose is never parsed as a record.

During brewing, read commands do not consume the invocation's single-mutation
claim. At most one successful knowledge mutation is accepted for each backend
invocation.

## Index behavior

`.state/index.sqlite` is a disposable derived index:

- existing feedstocks are not reparsed; only previously unseen IDs are added;
- knowledge is refreshed when its file mtime changes and removed when its file
  disappears;
- `PRAGMA user_version` controls schema compatibility;
- version mismatches, detected corruption, and `--reindex` cause a full rebuild
  from Markdown.

Deleting the index never deletes feedstock or knowledge.

## Data and security invariants

- Original session logs are read-only and are not copied.
- Feedstock is append-only. Only the one-time `brewed_at` processing field may
  be added later.
- Only human messages are stored as `user_quote`; assistant responses and tool
  outputs are not source text.
- Invalidated knowledge remains on disk for provenance.
- Every knowledge record requires at least one existing feedstock source.
- Unknown topic or subject vocabulary is added by the CLI as `pending`.
- File names and identifiers are validated, writes are atomic, and concurrent
  draw, brew, and index updates use non-blocking process locks.
- JSON string encoding separates retrieved data from agent instructions.

See [SECURITY.md](SECURITY.md) for the trust model.

## Releasing

Maintainers publish npm packages, a GitHub Release, and the version consumed by
`go install` by pushing a version tag:

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

Ordinary branch pushes and pull requests run tests only; they do not publish
artifacts.

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
```

Optional parser checks against real logs can be run without copying them:

```sh
KNOWBREW_TEST_CLAUDE_LOG=/path/to/session.jsonl \
KNOWBREW_TEST_CODEX_LOG=/path/to/rollout.jsonl \
go test -v ./internal/parser -run TestRealLogWhenConfigured
```

## License

MIT
