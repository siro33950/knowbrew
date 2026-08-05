# knowbrew

**Your coding agent forgets everything when the session ends.**

You explain the same context again. You correct the same mistake again. You
re-solve a problem you already solved last month, because the answer only ever
existed in a session you closed.

`knowbrew` turns your Claude Code and Codex session logs into durable knowledge:
plain Markdown files in a folder you own, searchable by your agent in future
sessions.

```sh
npm install -g knowbrew
knowbrew init
```

[日本語版 README](README.ja.md)

## What you get

- **Your agent can look things up.** Past decisions, conventions, corrections,
  and solutions become searchable records your agent reads when they are
  relevant — instead of you re-explaining them.
- **Nothing reaches your agent without your approval.** Everything knowbrew
  writes starts with `approved: false`, invisible to normal retrieval. You
  decide what to approve, so a bad inference stays harmless.
- **Plain Markdown in your own folder.** Point it at an Obsidian vault or any
  directory. Read it, edit it, grep it, sync it, delete it. No service, no
  account, no lock-in.
- **Works across Claude Code and Codex.** One knowledge base, both agents.
- **Everything traces back.** Every record cites the exact turns it came from,
  and your original logs are never modified or copied.

## How it works

The flow follows a brewing metaphor:

```text
session logs ──draw──▶ feedstock ──brew──▶ knowledge ──approve──▶ distill ──▶ documents
(untouched)             (what happened)       (durable claims)               (derived views)
```

**`draw`** reads your session logs and records one *feedstock* per turn: a short
summary plus the atomic claims that turn established. The raw dialogue stays in
the source log. A feedstock stores the agent and session ID, while knowbrew
resolves the log's current location from the configured source paths when raw
dialogue is needed.

**`brew`** reads that evidence and writes *knowledge*: claims that stay useful
beyond the turn they came from. Each record has one type and one subject, and
can later be corrected, replaced, or merged as your project evolves.

**You approve.** New knowledge starts with an unchecked `approved` property.
Check it in Obsidian, or change it to `approved: true` in Markdown. Only then
can your agent see it.

**`distill`** regenerates readable Subject documents from approved, current
knowledge. Each document follows a Template assigned by its Subject and records
the exact Knowledge IDs it used. Knowledge remains the source of truth; files
under `documents/` are reproducible derived views.

## Getting started

Run `init` in the directory that should hold your knowledge files — an Obsidian
vault folder works well:

```sh
knowbrew init
```

It asks which session logs to read, which LLM backend to use, and whether to
register itself with Claude Code and Codex. Registration adds a SessionStart
hook for approved Knowledge and a Stop hook that Draws the completed session
turn. Running `init` again seeds the form from your current configuration and
keeps settings it does not ask about.

Then build your knowledge base:

```sh
knowbrew draw    # session logs → feedstock
knowbrew brew    # feedstock → pending knowledge
# review and approve Knowledge, then:
knowbrew distill # approved Knowledge → Subject documents
```

The commands are safe to re-run: Draw and Brew resume unfinished work, while
Distill regenerates derived documents. Run them from a hook, from cron, or by
hand.

With no arguments, `draw` looks at configured session files modified in the last
24 hours. Use `--since 7d` (or an RFC3339 timestamp) to widen the window.
`--max N` safely works through historical data: it selects at most N
unfinished turns across all configured history, resumes already acquired turns
first, and then proceeds from newer turns to older ones. Repeating it from cron
eventually consumes the backlog without an unbounded LLM run:

```sh
knowbrew draw --max 100
knowbrew brew --max 100
knowbrew distill --max 2
```

The draw summary reports `turns_selected` for the current run and
`turns_pending` for the unfinished turns remaining in its source scope.
For Brew, `--max` counts unresolved Assertions; mechanical NOOP processing does
not consume the limit. For Distill, it counts Subject documents. Bounded
Distill runs continue from the next Subject and Template on the following run,
so repeated invocations rotate through all assigned documents even though each
document remains regenerable.

Review what was created, and promote what you want your agent to use:

```yaml
# knowledge/kn-8f17c9a6b4d2e301.md
approved: true   # was: false
```

Records marked `trigger: always` are injected at the start of every session
through the SessionStart hook that `init` registers. Everything else your agent
finds by searching.

## Daily use

Search your knowledge:

```sh
knowbrew knowledge -- sqlite locking
knowbrew knowledge --subject myproject --type decision
```

Look back at what actually happened:

```sh
knowbrew feedstock --last 10                      # the last 10 turns
knowbrew feedstock --subject myproject -- deploy  # when did we touch deploys?
knowbrew show <feedstock-id> --raw                # the original conversation
```

Keywords go after `--`. With keywords you get relevance ranking; without them
you get newest-first, which makes `feedstock` a readable timeline of what you
worked on.

When semantic search is enabled, keyword queries run FTS5 and vector search in
parallel and merge their ranks with reciprocal rank fusion. Knowledge vectors
contain only the claim; feedstock vectors contain only the summary. Exact
subject, type, lifecycle, agent, session, and time filters still apply. Search
scores are intentionally not exposed because they are ranks, not confidence.
Use `--search-mode text` or `--search-mode vector` only when diagnosing one
branch; the default is `hybrid`.

Your agent uses these same commands. `init` writes usage instructions into your
`CLAUDE.md` / `AGENTS.md` so it knows when to reach for them.

### Token usage

While `draw`, `brew`, or `distill` runs, the progress line shows cumulative input and output
tokens (`in ... / out ...`), and the final JSON includes a `usage` object with
the backend, model, and per-class token counts. Multiply those by your
provider's current rates to get the cost. knowbrew ships no price table because
provider prices change independently of the CLI.

## Knowledge types

Every knowledge record has exactly one type. Types are master notes under
`masters/types/`; their filenames are the values accepted by `--type`. `init`
creates these eight defaults:

| Type | What it holds |
|---|---|
| `definition` | The established meaning or boundary of a term or concept |
| `property` | A durable established attribute or capability, excluding temporary configuration, execution results, and task state |
| `relation` | An established relationship between subjects or concepts |
| `principle` | An established generalized cause, mechanism, or recurring tendency |
| `constraint` | An established externally imposed limit or required condition |
| `decision` | A settled choice intended beyond the current task, excluding tentative or one-time adjustments |
| `intent` | A durable intended outcome or quality that explains why a subject, rule, or design exists independently of its current implementation |
| `preference` | A stable stated preference, rather than a one-time request or binding decision |

Types are useful as filters (`--type decision` to review your decisions), and
they keep brewing honest: a claim is recorded only when it fits one type
exactly.

You can edit the master wording, add types, or delete unused ones. Defaults are
regenerated only when `masters/types/` has no type notes at all; if even one
remains, knowbrew leaves the directory untouched.

## Subjects

Subjects are stable target names stored as master notes under
`masters/subjects/`. knowbrew adds one automatically only when it can derive it
from a Git repository, recording the remote and working directory as aliases.

A subject note may contain `definition`, `includes`, `excludes`, and `documents`.
The `documents` property declares which documents should be generated for that
Subject. Each value is a wikilink to the corresponding definition under
`masters/templates/`; an empty list excludes that Subject from distillation.
The other fields decide what belongs to the subject; a name alone is the
fallback when they are absent, and an exclusion overrides a name match.

Only you can create subjects — knowbrew never invents one. An unknown
`--subject` is an error, and there is no `--new-subject` flag. Create, rename,
merge, or delete subject notes directly in your vault. Claims that match no
subject are kept aside, so editing a subject later can make them eligible.

## Distilled documents

`init` creates four starter Template masters: `concept`, `reference`,
`decisions`, and `glossary`. A Template describes the document's purpose,
readers, scope, completion criteria, output filename, and Markdown structure.
Declare the documents to generate in a Subject note:

```yaml
documents:
  - "[[concept]]"
  - "[[reference]]"
```

`knowbrew distill` checks every approved, current Knowledge record for each
requested Subject document, then writes
`documents/<subject>/<template-output>`. It removes outputs that no longer have
any valid supporting Knowledge. Use `--subject` or `--template` to limit a run.

## Configuration

`init` writes `<root>/.knowbrew/config.toml`:

```toml
root = ".."

[llm]
backend = "claude-cli"    # or codex-cli, api, ollama
draw_model = ""           # per-turn classification: prefer a fast model
brew_model = ""           # knowledge decisions: prefer a strong model
distill_model = ""        # document synthesis: prefer a strong model
draw_effort = "low"       # repeated classification: low is the init default
brew_effort = ""          # empty uses the backend or user default
distill_effort = "high"   # document selection and synthesis
timeout = "5m"

[draw]
concurrency = 5           # parallel LLM workers
context_turns = 3         # earlier dialogue turns given to the extractor
max_context_turns = 20    # bounded fallback window

[embedding]
model = "ruri-v3-130m-int8-onnx" # or snowflake..., qwen3..., disabled, custom
# path = "/absolute/path/to/model" # required only for custom

[[sources]]
agent = "claude"
parser = "claude"
paths = ["/Users/example/.claude/projects"]

[[sources]]
agent = "codex"
parser = "codex"
paths = [
  "/Users/example/.codex/sessions",
  "/Users/example/.codex/archived_sessions",
]
```

Each source is one logical collection and can span multiple directories.
`init` configures both the active and archived Codex session directories.
Feedstocks do not store these physical paths, so moving a session between the
configured directories does not break `show --raw`, Draw resume, or Brew.

Empty model values use the CLI backend's own default. `api` and `ollama` require
all three models and read credentials from the environment:

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

Keys are read from the environment only and are never written into your
knowledge root. `~/.config/knowbrew/location.toml` records where your root is,
so knowbrew works from any directory.

CLI backends run your own agent, so your `CLAUDE.md` / `AGENTS.md` apply: what
knowbrew writes follows your instructions, including the language you write in.
Your MCP servers are not loaded for these background jobs. knowbrew's own hooks
exit immediately inside them, preventing recursive Draws and Knowledge lookup.

`init` offers Japanese-recommended Ruri, English-recommended Snowflake,
quality-first Qwen3, or disabled full-text-only search. knowbrew downloads and
pins the selected managed model and runtime under `.knowbrew/state/models/`.
Configured managed or custom model files must be usable; knowbrew never hides a
model error by silently falling back to text search.

For a self-managed model, set `model = "custom"` and point `path` at a directory
containing `manifest.json`. The manifest requires `id`, `backend`, `dimension`,
and `model_file`; ONNX additionally uses `tokenizer_file`, `runtime_file`,
`input_names`, and `output_name`, while llama.cpp uses `executable_file`.
Manifest file paths are relative to that directory.

## Requirements

- One LLM backend: Claude Code CLI, Codex CLI, an OpenAI-compatible endpoint
  with tool calling, or Ollama with a tool-capable model
- Go 1.25 or later and a C compiler only if you build from source (FTS5 uses
  pure Go; sqlite-vec is statically linked through its official Go binding)

Alternative installs:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

Prebuilt binaries are on
[GitHub Releases](https://github.com/siro33950/knowbrew/releases).

## Command reference

```text
knowbrew init                      interactive setup
knowbrew draw [flags] [path...]    session logs → feedstock
knowbrew brew [flags]              feedstock → pending knowledge
knowbrew distill [flags]           approved knowledge → Subject documents
knowbrew knowledge [keywords...]   search knowledge (alias: kn)
knowbrew knowledge show <id...>    inspect knowledge in any lifecycle state
knowbrew feedstock [keywords...]   search or replay feedstock
knowbrew show <id...>              one feedstock record; --raw for the dialogue
knowbrew index sync|rebuild|status maintain the derived search indexes
```

Shared search flags: `--subject`, `--type`, `--since`, `--until`, `--limit`,
`--max-tokens`, `--reindex`, `--search-mode`. `knowledge` adds `--include-pending`,
`--include-retired`, and `--trigger always`. `feedstock` adds `--session`,
`--agent`, and `--last N`.

`draw` flags: `--max N`, `--since`, `--until`, `--source claude|codex`,
`--verbose`. Explicit files and directories must be inside a configured source
path. They are never time-limited unless you also pass `--since` or `--until`.

`brew` flags: `--max N`, `--verbose`.

`distill` flags: `--max N`, `--subject`, `--template`, `--verbose`.

Some subcommands exist only for the LLM backend to call and are not meant for
direct use.

## Security

Your session logs are read-only, generated knowledge stays unapproved until you
check it, and API credentials are read from environment variables only. See
[SECURITY.md](SECURITY.md) for the trust model and how to report a
vulnerability.

## Development

```sh
go test ./...
go vet ./...
gofmt -l .
```

Parser checks against real logs, without copying them:

```sh
KNOWBREW_TEST_CLAUDE_LOG=/path/to/session.jsonl \
KNOWBREW_TEST_CODEX_LOG=/path/to/rollout.jsonl \
go test -v ./internal/parser -run TestRealLogWhenConfigured
```

Maintainers release by pushing a version tag; ordinary pushes only run tests.

```sh
git tag vX.Y.Z
git push origin vX.Y.Z
```

## License

MIT
