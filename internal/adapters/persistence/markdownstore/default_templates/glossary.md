---
description: Organizes the semantic meaning and boundaries of terms and concepts needed to understand the subject.
output: glossary.md
purpose: Help readers interpret each term consistently and distinguish related concepts.
readers:
  - Everyone who reads or writes about the subject
covers:
  - The essential meaning of terms and concepts needed to understand the subject
  - Each concept's role and semantic boundaries relative to other concepts
  - Aliases or spelling variants when supported by evidence
  - Terms or concepts with a direct semantic relationship
excludes:
  - Guessed definitions not established in Knowledge
  - Extended explanations of the subject itself
  - Storage or representation details such as files, frontmatter, fields, paths, or data structures
  - Implementation or operational details such as algorithms, processing order, defaults, configuration, CLI behavior, or validation rules
  - Terms supported only by implementation or operational details and lacking an established semantic definition
  - A name-only index
completion:
  - Orders terms consistently for lookup, using alphabetical order by display term unless another order is more appropriate
  - Makes the first sentence of each entry sufficient to understand what the term denotes
  - Explains what each concept means rather than how it is implemented
  - Defines each concept once and does not split aliases or spelling variants into separate entries
  - Avoids circular definitions
  - Includes distinctions and related terms only when grounded in Knowledge
  - Omits terms that can be described only through implementation or operational details
  - Keeps each definition to one or two short paragraphs in most cases
  - Grounds every statement in Knowledge without filling gaps by inference
---

# {{subject glossary title}}

<!-- Render the document title in the output language. -->

## {{term}}

<!--
Define what the term denotes in a self-contained first sentence.
Only when needed, use a second paragraph to clarify the concept's role, semantic boundary, or aliases.
Do not describe how the concept is stored, represented, processed, configured, or validated. Those details belong in Reference.
Omit the entire entry when the evidence establishes only implementation or operational details and does not establish semantic meaning.
-->

**{{distinction label}}:** {{semantic distinction from a commonly confused concept}}

<!-- Include this entire line only when the distinction is grounded in Knowledge. Render the label in the output language. -->

**{{related terms label}}:** {{directly related terms}}

<!-- Include this entire line only when the relationship is grounded in Knowledge. Render the label in the output language. -->
