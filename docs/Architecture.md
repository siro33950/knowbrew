# Architecture

## Policy

knowbrew uses a small-scale Clean Architecture suited to a Go CLI.

The goal is not to add layers or abstractions mechanically. The goal is to separate business rules from external I/O so that code with different reasons to change does not affect each other.

## Dependency direction

Dependencies always point inward.

```text
cmd / adapters
      ↓
application
      ↓
domain
```

An inner layer must not import an outer layer.

## Layers

### Domain

Contains knowbrew-specific concepts, invariants, and state transitions.

- Does not know about external I/O, configuration, CLI concerns, persistence formats, or LLMs.
- Remains testable without external systems.
- Feature design starts by identifying domain concepts and rules.

### Application

Defines use-case sequencing and transaction boundaries.

- Depends only on the Domain and ports defined by the Application layer.
- Does not construct or select concrete adapters.
- Passes external input to the Domain and applies Domain results to external systems.

### Adapters

Implement external boundaries such as the CLI, LLMs, sources, persistence, search, and presentation.

- Implement Application ports.
- Convert between external representations and Application or Domain types.
- Do not contain business rules or use-case sequencing.

### cmd

Acts as the composition root: it loads configuration, assembles concrete adapters, and starts Application use cases.

- Does not contain business rules.
- Does not manipulate Domain objects or persistence directly.

## Boundary rules

- Define interfaces on the consumer side with only the operations the consumer needs.
- Do not leak external representation types into the Domain or Application layers.
- Do not reimplement Domain invariants in adapters or persistence code.
- Do not pass the entire configuration into an Application use case; pass only the values it needs.
- Treat LLM output as external input and never persist it without Domain validation.
- The Application defines the scope of atomic operations; adapters implement locking and atomic writes.
- Do not create large `common`, `util`, or `service` packages merely to share code.
- Do not add interfaces, DTOs, or repositories mechanically for every concept.

## Exceptions

Any exception to the dependency direction or layer responsibilities requires an explicit agreement on its reason and impact before implementation.
