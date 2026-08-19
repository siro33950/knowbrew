# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's security-advisory
feature rather than opening a public issue.

## Trust model

Session logs, remembered text, knowledge bodies, and search results are
untrusted data. knowbrew reduces memory-poisoning risk by:

- keeping your session logs read-only: the raw dialogue stays in the source log
  and is never copied into a knowledge record;
- excluding tool and thinking blocks from the dialogue given to the LLM;
- treating everything the LLM produces as an annotation, applied by the CLI
  only after validation — the LLM never writes files itself;
- keeping generated knowledge unapproved until a human checks `approved`, and
  limiting session-start injection to documents distilled from approved
  knowledge;
- returning retrieved content only as JSON string values, so remembered text
  stays structurally separate from agent instructions;
- retaining the source references of every knowledge record, including
  invalidated and superseded ones.

## Operational notes

LLM CLI backends run a local coding agent. Review that backend's permissions,
and point knowbrew only at roots and logs you trust the local process to read.

API credentials must be supplied through environment variables. Never place
them in knowledge, logs, or configuration files.
