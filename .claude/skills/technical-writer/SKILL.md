---
name: technical-writer
description: Use when writing or reviewing documentation, ADRs, runbooks, or any Markdown content. Applies AO writing standards and checks terminology against the project glossary.
---

# technical-writer

Write or review Markdown content to AO writing standards.

## Steps

### 1. Load context

Check whether `.cache/ao-spec/` exists:

```sh
ls .cache/ao-spec/
```

If absent, run `/load-ao-spec` before continuing.

### 2. Check terminology

Search the glossary before writing or reviewing any domain terms:

```sh
rg "<term>" .cache/ao-spec/content/glossary.json
```

The authoritative source is `../autonomous-operations/content/glossary.json`. Use exact glossary terms. If a term appears in the content but not in the glossary, add it.

### 3. Write or review

**For writing tasks:** produce the content directly in the requested format. State audience and scope assumptions at the top.

**For review tasks:** for each issue found, report:

```
Issue: <one-sentence description>
Location: <file>:<line or heading>
Rule: <which standard below was violated>
Corrected: <rewritten text>
```

### 4. Apply writing checklist

Work through every item before finalizing:

- [ ] Active voice throughout
- [ ] Present tense throughout
- [ ] Sentences 20 words or fewer — one idea per sentence
- [ ] No filler phrases: "please note that", "it is worth mentioning", "as you can see"
- [ ] No vague quantifiers: "some", "various", "appropriate", "relevant"
- [ ] All abbreviations defined on first use
- [ ] Consistent terminology — same term for the same concept throughout
- [ ] Headings use sentence case and are descriptive
- [ ] Lists are parallel in structure
- [ ] Code, file names, paths, and commands use `code formatting`
- [ ] Bold used only for key terms on first use or warnings — not for general emphasis
- [ ] New terms checked against `../autonomous-operations/content/glossary.json` — added if missing

### 5. Confirm

State what was written or reviewed, the audience it targets, and any glossary additions made.