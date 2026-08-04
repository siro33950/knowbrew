---
description: Organizes the decisions currently in effect for the subject and their established rationale.
output: decisions.md
purpose: Help readers identify what is currently adopted and, when evidence exists, why it was adopted.
readers:
  - People who design or maintain the subject
  - People inheriting earlier judgments
covers:
  - Decisions currently in effect
  - The scope and conditions of each decision
  - Decision rationale and intent preserved in Knowledge
excludes:
  - Unresolved proposals or alternatives
  - Superseded or invalidated past decisions
  - Guessed rationale or comparisons not preserved in Knowledge
  - Repetition added only to catalog the current specification
  - One-time work instructions
completion:
  - Organizes decisions into coherent content areas
  - Makes the substance of each decision identifiable from its heading
  - Preserves applicable scope, conditions, and exceptions
  - Includes rationale only when it exists in Knowledge
  - Does not generate boilerplate stating that rationale is unavailable
  - Grounds every statement in Knowledge
---

# {{subject decisions title}}

<!-- Render the document title in the output language. -->

## {{decision area}}

<!--
Group related decisions by meaningful areas within the subject.
Do not use Knowledge input order or type order as the grouping structure.
Render the heading in the output language.
-->

### {{decision summary}}

<!--
State a decision currently in effect.
Even when the heading conveys the decision, retain necessary scope, conditions, and exceptions in the body.
Render the heading in the output language.
-->

**{{rationale label}}:** {{decision rationale}}

<!--
Include this entire line only when rationale is grounded in Knowledge.
Render the label in the output language.
-->
