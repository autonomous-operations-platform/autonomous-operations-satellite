# AI Agent Instructions

> This file contains both the base rules for all AO repositories and the
> project-specific context for this repository. The project-specific sections
> are filled in by `/setup-repo` after cloning.

---

## Language and writing standards

These apply to all Markdown, comments, and documentation produced in this repository.

- **US English** throughout. Prefer plain language over idiomatic expressions —
  most readers and authors are not native English speakers.
- **Active voice.** "The system sends a request" — not "A request is sent."
- **Present tense.** "The component writes to SRS" — not "will write."
- **Short sentences.** One idea per sentence, 20 words or fewer as a guideline.
- **RFC 2119 compliance.** Use MUST, MUST NOT, SHOULD, SHOULD NOT, MAY in uppercase
  with their defined meanings. Each requirement using these keywords must define
  specific, measurable conditions.
- **Consistent terminology.** Use the same term for the same concept throughout.
- **Hyphenate compound modifiers:** `air-gapped`, `bare-metal`, `mission-critical`,
  `time-series`, `rule-based`, `real-time`, `zero-trust`.
- **No filler phrases:** "please note that", "it is worth mentioning", "as you can see."
- **Define abbreviations on first use:** "Satellite Record Store (SRS)."

---

## Security non-negotiables

- No hardcoded credentials of any kind.
- No plaintext secrets in runbooks, configuration files, or comments.

Run the `security-reviewer` sub-agent to check security compliance before merging.

---

## Version control

Use the `/commit` skill to stage and commit changes. It enforces branch naming,
commit format, scope requirements, and pre-commit checks.

---

## Token efficiency

AGENTS.md loads into every session. Bloated context degrades adherence and increases cost.

### Reading files

- **Search before read.** Locate the relevant file and line range before reading.
  Never load a full file to find one symbol.
  Preferred tools, in order: `rg` (ripgrep), `fd`, `grep -r`. Use `fzf` for
  interactive disambiguation when multiple candidates match.
- **Use line ranges.** Read only the lines you need. Re-read with a wider range if
  the first read is insufficient.
- **Do not re-read files you just edited.** The edit tool confirms success; a follow-up
  read is redundant unless the change depends on dynamic output.
- **Do not load files for background context.** Only read files directly needed for
  the current task.

### Context cache

If `.cache/ao-spec/` exists, it contains a sparse clone of the AO central
documentation (ADRs, architecture, specs). Use it for grounding before relying
on internal knowledge.

**Always search before reading** — the cache can be large:

```sh
rg "term" .cache/ao-spec/          # search file contents (preferred)
fd "pattern" .cache/ao-spec/       # search by filename or path
grep -r "term" .cache/ao-spec/     # fallback if rg/fd unavailable
```

Read only the files the search identifies as relevant. The cache is read-only —
never write, edit, or commit files inside `.cache/ao-spec/`.

To populate or refresh the cache, run `/load-ao-spec`.

### Tool calls

- **Batch independent calls.** When multiple tool calls have no dependency on each
  other, issue them in a single message so they run in parallel.
- **Do not verify the obvious.** After writing a file, do not immediately read it
  back. After a lint command passes, do not re-run it.
- **Do not ask for confirmation on unambiguous tasks.** If the intent is clear, act.
  Reserve clarifying questions for genuine ambiguity.

### Output

- **Do not echo diffs or file content back in chat.** The editor shows changes;
  repeating them as code blocks or inline diffs wastes output tokens and adds noise.
  Summarize what changed in one sentence instead.
- **Do not narrate tool calls.** "I will now read the file…" adds no value. Act, then
  report the outcome if relevant.

### Stopping

- **Stop when done.** A task is complete when the requested change is made and tests
  pass. Do not add unrequested improvements, refactors, or documentation.
- **One attempt per approach.** If a fix fails, diagnose before retrying. Repeating
  the same action expecting a different result wastes tokens and context.

---

## Sub-agents

For tasks that require a focused persona, invoke a sub-agent from `.claude/agents/`:

| Sub-agent | When to invoke |
| --- | --- |
| `developer` | Writing or reviewing code: coding standards, dependency rules, branch naming |
| `architect` | Evaluating design decisions, ADRs, cross-component changes, or planning a non-trivial task |
| `security-reviewer` | Pre-merge security review and compliance verification |
| `code-reviewer` | Structured code review against the team checklist |
| `threat-modeler` | Design-time threat modeling for new components or data flows |
| `devils-advocate` | Adversarial challenge of a plan, design, or decision |

### Agent interaction guide

Sub-agents hand off to each other. Use these chains for common workflows:

**New feature or task**

```
architect (plan) → developer → code-reviewer → security-reviewer
```

**New component or service (with security surface)**

```
architect (plan) → threat-modeler → developer → code-reviewer → security-reviewer
```

**Architecture decision or ADR**

```
architect → devils-advocate → architect (revise) → technical-writer (record ADR)
```

**Pre-merge review**

```
code-reviewer → security-reviewer
```

**Any significant design or plan before committing**

```
<any agent> → devils-advocate
```

**Documentation or runbook**

```
technical-writer (standalone, or after architect)
```

Rules:
- Enter plan mode or invoke `architect` before starting any non-trivial task. Do not invoke `developer` without a plan unless the task is unambiguously small.
- `devils-advocate` challenges output from any agent. Invoke it before finalizing a design, ADR, or plan — not after implementation has begun.
- `threat-modeler` pairs with `architect`, not `security-reviewer`. Threat modeling is design-time; security review is pre-merge code-time.
- `security-reviewer` is always the last step before merge. No agent output bypasses it for security-sensitive changes.
- `/technical-writer` is invoked after content is structurally complete — not mid-draft.