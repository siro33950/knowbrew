# Security

## Reporting a vulnerability

Please report security issues privately through GitHub's security-advisory
feature rather than opening a public issue.

## Trust model

Session logs, remembered user text, knowledge bodies, and search results are
untrusted data. knowbrew reduces memory-poisoning risk by:

- storing only human user text as feedstock originals;
- making LLM writes pass through validated CLI commands;
- keeping generated knowledge pending until a human activates them;
- limiting SessionStart injection to active `trigger: always` knowledge;
- returning retrieved content only as JSON string values;
- retaining source-feedstock references for every knowledge.

LLM CLI backends still execute a local coding agent. Review the backend's
permissions and run knowbrew only against roots and logs you trust the local
process to read. API credentials must be supplied through environment
variables and should never be placed in knowledge, logs, or configuration files.
