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

Three layers, following a brewing metaphor:

```text
session logs  ──draw──▶  feedstock  ──brew──▶  knowledge
(untouched)              (what happened,       (what is worth
                          one per turn)         remembering)
```

**`draw`** reads your session logs and records one *feedstock* per completed or
interrupted turn. A still-running final turn is left in the source log and is
acquired by a later draw only after its terminal record appears. Its
acquisition phase writes only mechanical source identity and environment fields.
It then completes two separate LLM phases. First, all missing summaries are
created in parallel from only each target turn's user input and agent response.
After every summary finishes, all missing Assertion sets are extracted in
parallel. Assertion extraction receives the target user input and earlier turns
only; it never receives the target agent response, generated summary, or future
turns. If the initial earlier window cannot resolve a target reference, the
extractor may load one larger bounded earlier window. The raw dialogue stays in
the source log. An agent response establishes an Assertion only when a later
user turn explicitly adopts, corrects, or rejects it.
The classification agents return schema-validated structured results; they do
not write Feedstock files. The draw process validates those results and performs
each Store transition itself.

Each Assertion has exactly one type, an explicit subject string, and a
self-contained statement, with optional rationale and trigger. An empty subject
string means that no current subject master matches.
Draw matches each atomic meaning against every subject master. If several
subjects match, it emits one Assertion per subject; if none match, it keeps one
subjectless Assertion for later reclassification rather than creating
subjectless Knowledge.
Feedstock `types` and `subjects` are mechanically derived from its Assertions,
so an empty Assertion set remains an efficient mechanical Brew filter and no
independent Feedstock classification can disagree with its body.

**`brew`** reads that evidence and writes *knowledge*: established semantic
claims that remain useful beyond the source turn and can later be corrected,
replaced, or distilled independently. For each subjectful Assertion in source
order, Brew verifies it against the original dialogue, loads the exact subject's
Knowledge catalog, reads every plausible relation in full, and returns only the
semantic relation: `equivalent`, `complements`, or `conflicts`. Type-master
definitions are the sole authority for semantic eligibility in both Draw and
Brew; neither stage adds a separate hard-coded category exclusion list.

Atomic means independently changeable, not merely “written as one sentence.”
If A could later be corrected or invalidated while B remains true, A and B are
separate records. Conditions, scope limits, and exceptions that determine
whether A is true stay in A's Claim.

**You approve.** New knowledge starts with an unchecked `approved` property.
Check it in Obsidian, or change it to `approved: true` in Markdown. Only then
can your agent see it.

## Getting started

Run `init` in the directory that should hold your knowledge files — an Obsidian
vault folder works well:

```sh
knowbrew init
```

It asks which session logs to read, which LLM backend to use, and whether to
register itself with Claude Code and Codex. Running `init` again seeds the form
from the current configuration, preserves settings it does not ask about and
custom sources, and fills defaults only for newly introduced missing keys.

Then build your knowledge base:

```sh
knowbrew draw    # session logs → feedstock
knowbrew brew    # feedstock → pending knowledge
```

Both are idempotent and safe to re-run. With no paths or flags, `draw` reparses
configured session files modified in the last 24 hours, derives deterministic
feedstock IDs, ignores the still-running terminal turn, writes only missing records, summarizes only records without a
summary, and extracts Assertions only from summarized unannotated records in
those selected sessions. It keeps no acquisition cursor or
per-session state. Concurrent `draw` processes wait for one another and then
recheck the same window, so overlapping hooks do not duplicate acquisition or
classification. Run the commands from a hook, from cron, or by hand.

`brew` processes the Assertions already stored in Feedstock; it has no separate
candidate files or extraction pass. Source verification may preserve an
Assertion, correct its wording without changing its ID or subject, or reject and
delete it. The CLI records each resolved Assertion ID in `brewed_assertions`.
After interruption, Knowledge assertion references repair any write completed
before that marker, and the next run processes only unresolved subjectful
Assertions. Subjectless Assertions remain unbrewed so a later subject edit can
make them eligible.

While either command is running, its progress line shows cumulative input and
output tokens separately (`in ... / out ...`). The final JSON includes a
`usage` object with the backend, model, standard input, cache-read input,
cache-write input, output, and total token counts. These fields can be
multiplied by the current provider rates to calculate API cost. knowbrew does
not embed a price table because provider prices change independently of the
CLI; a token class that the backend does not report is returned as zero.

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
worked on. Subject identifies the concrete target; cross-cutting themes are
found by full-text search instead of a separate controlled vocabulary.
Feedstock full-text search indexes its summary and Assertions; use `show --raw`
when you need the untouched dialogue from the source log.

Your agent uses these same commands. `init` writes usage instructions into your
`CLAUDE.md` / `AGENTS.md` so it knows when to reach for them.

## Knowledge types

Every knowledge record has exactly one type. Types are master notes under
`masters/types/`; their filenames are the values accepted by `--type`, and
their `definition` and optional `example` fields are supplied to the
classification and brewing agents. `init` creates these seven defaults:

| Type | What it holds |
|---|---|
| `definition` | The established meaning or boundary of a term or concept |
| `property` | A durable established attribute or capability, excluding temporary configuration, execution results, and task state |
| `relation` | An established relationship between subjects or concepts |
| `principle` | An established generalized cause, mechanism, or recurring tendency |
| `constraint` | An established externally imposed limit or required condition |
| `decision` | A settled choice intended beyond the current task, excluding tentative or one-time adjustments |
| `preference` | A stable stated preference, rather than a one-time request or binding decision |

Types are useful as filters (`--type decision` to review your decisions), and
they keep brewing honest: qualification happens before atomic splitting, and an
Assertion is written only when its semantic meaning fits one type exactly.
Acquisition labels such as feedback and final document forms such as a wiki or
runbook are not knowledge types. You can edit the master wording, add types, or
delete unused types. Defaults are regenerated only when `masters/types/` has no
type notes at all; if even one remains, knowbrew leaves the directory untouched.

## Subjects

Subjects are stable target names stored as master notes under
`masters/subjects/`. knowbrew automatically adds a subject master only when it
can mechanically derive one from a Git repository. The repository remote and
working directory are recorded as aliases of that master.
This mechanical step only prepares the vocabulary; it never assigns a subject
to a Feedstock. Draw prefers an explicit target in the dialogue and uses the
repository as the implicit target only when the owner is omitted and the claim
is about the system being worked on.

A subject note may contain `definition`, `includes`, and `excludes`. Draw and
Brew use those semantic fields as the authority for subject boundaries; a name
alone is the fallback when they are absent, and an exclusion vetoes a name
match. `aliases` are machine lookup keys only and are never supplied as semantic
evidence to the LLM.

Draw may match only existing subject masters or omit the subject; Brew must
preserve the Assertion's subject. Neither agent can create one. An unknown
`--subject` is an error, and there is no `--new-subject` flag. You can still
create, rename, merge, or delete subject master notes directly in the vault.

## Configuration

`init` writes `<root>/.knowbrew/config.toml`:

```toml
root = ".."

[llm]
backend = "claude-cli"    # or codex-cli, api, ollama
draw_model = ""           # per-turn classification: prefer a fast model
brew_model = ""           # knowledge decisions: prefer a strong model
draw_effort = "low"        # repeated classification: low is the init default
brew_effort = ""           # empty uses the backend or user default
timeout = "5m"

[draw]
concurrency = 5           # workers used independently by each LLM phase
context_turns = 3         # earlier dialogue turns supplied to Assertion extraction
max_context_turns = 20    # one bounded earlier fallback window

[[sources]]
agent = "claude"
path = "/Users/example/.claude/projects"
parser = "claude"
```

Empty model values use the CLI backend's own default. `api` and `ollama` require
both models and read credentials from the environment:

```sh
export KNOWBREW_API_URL=https://api.example.com/v1/chat/completions
export KNOWBREW_API_KEY=...
```

Keys are read from the environment only and are never written into your
knowledge root. `~/.config/knowbrew/location.toml` records where your root is,
so knowbrew works from any directory.

CLI backends run your own agent, so your `CLAUDE.md` / `AGENTS.md` apply: what
knowbrew writes follows your instructions, including the language you write in.
Your MCP servers are not loaded for these background jobs, and knowbrew's own
SessionStart hook does not fire inside them.

## Requirements

- One LLM backend: Claude Code CLI, Codex CLI, an OpenAI-compatible endpoint
  with tool calling, or Ollama with a tool-capable model
- Go 1.25 or later only if you build from source (SQLite FTS5 ships as pure Go;
  CGO is not required)

Alternative installs:

```sh
go install github.com/siro33950/knowbrew/cmd/knowbrew@latest
```

Prebuilt binaries are on
[GitHub Releases](https://github.com/siro33950/knowbrew/releases).

## Command reference

```text
knowbrew init                     interactive setup
knowbrew draw [flags] [path...]      session logs → feedstock
knowbrew brew [--verbose]            feedstock → pending knowledge
knowbrew knowledge [keywords...]  search knowledge (alias: kn)
knowbrew knowledge show <id...>  inspect knowledge in any lifecycle state
knowbrew feedstock [keywords...]  search or replay feedstock
knowbrew show <id...>             one feedstock record; --raw for the dialogue
```

Shared search flags: `--subject`, `--type`, `--since`, `--until`,
`--limit`, `--max-tokens`, `--reindex`. `knowledge` adds `--include-pending`,
`--include-retired`, and `--trigger always`. `feedstock` adds `--session`,
`--agent`, and `--last N`.

`draw` with no arguments scans configured session files modified during the
last 24 hours. The modification window selects files; each selected session is
parsed in full so adjacent-turn context remains available. Use `--since 6h`,
`--since 7d`, or an RFC3339 timestamp/date to change the window, `--until` to
set its other boundary, and `--source claude` or `--source codex` to limit
configured sources. `--all` is required to scan all configured history. An
explicit file or directory path is never time-limited unless you also pass
`--since` or `--until`.

Acquisition is keyed by agent, session ID, and turn ID, so reparsing overlapping
windows or moving a log does not create duplicates. Summarization and Assertion
extraction are limited to incomplete feedstocks from sessions selected by the current invocation;
unrelated old pending records are not swept in.

The LLM backend may use only three read operations: `feedstock context`,
`knowledge catalog`, and `knowledge show`. Summary, Assertion, and Brew agents
return schema-validated structured results without a target Feedstock or
Assertion ID. The parent draw or brew process owns that target, validates the
result, and performs the single Store mutation. It also derives Feedstock
`types` and `subjects`, assigns deterministic Assertion IDs, and renders the
Feedstock body. Brew handles one Assertion per invocation. The catalog is
discovery only: every relation target must then be read in full in that same
invocation before the structured decision is returned. The parent rereads current files under
lock, applies source-time guards, generates the Knowledge ID and filename, and
mechanically chooses evidence addition, pending revision, pending successor,
consolidation, or creation.

Semantic recency comes from source events, not Markdown file mtimes.
Knowledge recency is the newest timestamp among all supporting feedstocks.
A historical Assertion may add equivalent evidence to a matching version, but
cannot merge with, conflict with, or replace newer Knowledge. Knowledge search
returns the effective source time as `established_at`.

Approval never depends on a CLI event. A later search, hook lookup, or brew run
observes direct edits to `approved`. When an approved record has `supersedes`
links, its predecessors are durably marked with `superseded_by` and excluded
from normal retrieval while remaining on disk. A pending successor never
retires an active predecessor until the human approves the successor; pending
predecessors can be retired immediately. Legacy records with a `status` field
remain readable and are not bulk-rewritten.

## Design guarantees

- Your session logs are read-only. knowbrew never modifies or copies them.
- Feedstock mechanical fields, summary, and annotation time are immutable once
  written. Brew may correct or reject an Assertion only after checking the
  original dialogue; `types` and `subjects` are then recomputed mechanically.
- Pending Knowledge may be revised in place before human approval. Active
  Knowledge is never overwritten: replacements are pending successors, and an
  approved successor retires its predecessor as `superseded` while retaining
  both files.
- Every knowledge record has a stable ID used as its filename and cites both
  supporting Feedstocks and exact Assertion headings through Obsidian wikilinks.
- Retrieval returns JSON, keeping remembered content structurally separate from
  agent instructions.
- Runtime locks, the search index, and invocation claims live under
  `<root>/.knowbrew/state/`. Feedstock and Knowledge remain authoritative.

See [SECURITY.md](SECURITY.md) for the trust model.

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
