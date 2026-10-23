---
name: evolve-agent
description: Updates a sub-agent persona file with project-specific knowledge: where to find source files, domain models, definitions, documentation, and conventions. Run when a sub-agent's output lacks project context or after significant structural changes to the repository.
---

# evolve-agent

Add project-specific knowledge to a sub-agent persona file so it can work
effectively in this repository without requiring repeated prompting.

## When to run

- A sub-agent produces generic output that ignores project structure.
- A new sub-agent was added and has not been grounded in the project yet.
- The repository structure, key files, or conventions changed significantly.
- A retrospective surfaced repeated context that the team has to re-supply each session.

## Steps

### 1. Identify the target sub-agent

Ask the user which sub-agent to update if not already specified:

- `developer` — coding standards, directory layout, build commands
- `architect` — ADR locations, architecture boundaries, integration points
- `security-reviewer` — security-sensitive paths, compliance requirements, secrets management
- `code-reviewer` — review conventions, test patterns, coverage thresholds
- `technical-writer` — glossary location, doc structure, audience conventions
- `threat-modeler` — trust boundaries, sensitive data assets, existing security controls
- `devils-advocate` — known failure modes, past decisions that proved wrong, risk appetite

The file lives at `.claude/agents/<name>.md`.

### 2. Read the current file

Read `.claude/agents/<name>.md` in full before making any changes.

### 3. Gather project-specific knowledge

Explore the repository to find relevant anchors for the target sub-agent. Use
search tools rather than loading files speculatively:

```sh
# Find key domain model definitions
rg "type|struct|class|interface|model" --type go -l   # Go
rg "interface|type|class|schema" --type ts -l          # TypeScript
rg "class|dataclass|BaseModel" --type py -l            # Python

# Find documentation and spec references
fd "README|ARCHITECTURE|DESIGN|ADR|spec" --type f

# Find test conventions
fd "_test|.test|.spec" --type f | head -20

# Find main entry points
fd "main|index|app|server|handler" --type f
```

Read only the files the search identifies as directly relevant. Do not load full
files — use line ranges.

Also ask the user for any knowledge that cannot be derived from the codebase:

- External API contracts or service boundaries
- Links to design documents, specs, or runbooks not in the repo
- Compliance or regulatory constraints specific to this project
- Conventions agreed by the team that are not yet written down

### 4. Write the project context section

Append a `## Project context` section to the sub-agent file. Do not modify any
existing content.

Structure the section based on what is relevant for the sub-agent's role:

```markdown
## Project context

### Key files and locations

- `<path>` — <one-line purpose>
- `<path>` — <one-line purpose>

### Domain models and definitions

- `<Type/Interface name>` — defined in `<path>:<line>`. <one-sentence description>.

### Documentation and specs

- [<title>](<url or path>) — <one-line description>

### Conventions

- <Convention the agent must follow in this repo, not already in AGENTS.md>
```

Only include subsections that have content. Omit empty subsections.

Rules for content:

- File paths MUST be repo-relative (e.g. `internal/writer/writer.go`).
- Line numbers MUST be verified from the current file state before writing.
- External URLs MUST be provided by the user or confirmed reachable — do not guess.
- Do not duplicate content already in `AGENTS.md`.
- Keep each entry to one line. This section is a lookup index, not documentation.

### 5. Confirm

Print one summary line per file written:

```
evolve-agent: .claude/agents/<name>.md updated (<N> entries added)
```

Do not print file contents or diffs.