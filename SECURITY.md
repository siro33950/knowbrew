# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's security-advisory
feature rather than opening a public issue.

## Trust model

Session logs, remembered user text, knowledge bodies, and search results are
untrusted data. knowbrew reduces memory-poisoning risk by:

- leaving the raw dialogue in the read-only source log instead of copying it
  into Feedstock;
- mechanically excluding tool and thinking blocks before supplying the target
  dialogue and bounded adjacent dialogue to annotation, or returning dialogue
  through `show --raw`;
- treating Feedstock `summary` and Assertions as LLM-generated annotations,
  not source text;
- making classification use one validated annotation command and brewing verify
  one persisted Assertion at a time before a relation-only submission that the
  CLI applies mechanically with source-time guards;
- requiring every Brew relation target to appear in the exact-subject catalog
  and be read in full during the same invocation;
- keeping generated knowledge unapproved until a human checks `approved`;
- limiting SessionStart injection to approved, effective-active
  `trigger: always` knowledge;
- returning stored and raw retrieved content only as JSON string values;
- retaining feedstock references for every knowledge, including invalidated and
  superseded records.

Invocation read/operation claims, locks, and the disposable index are stored
under `<root>/.knowbrew/state/`; they are not Knowledge and are never injected
into an agent session.

LLM CLI backends still execute a local coding agent. Review the backend's
permissions and run knowbrew only against roots and logs you trust the local
process to read. API credentials must be supplied through environment
variables and should never be placed in knowledge, logs, or configuration files.
