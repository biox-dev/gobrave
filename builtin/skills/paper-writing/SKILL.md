---
name: paper-writing
description: Use this skill whenever the user asks you to write, expand, revise, or edit any part of a manuscript, grant proposal, fellowship application, or research statement. Defines the SNAP group's academic writing style (concise, claim-first, no padding) and the mechanics of iterating on Overleaf projects via local git sync and in-place bracketed instructions. Trigger on: "paper", "manuscript", "grant", "proposal", "fellowship", "abstract", "intro", "methods", "discussion", "reviewer response".
version: 1.0
---

# Paper & Grant Writing

This skill has two parts: **how to write** (voice, sentence-level rules, structural principles) and **how to iterate** (Overleaf git sync, bracketed instructions, section-by-section workflow).

The writing style section is the primary contribution of this skill. Follow it strictly — it is not a suggestion.

---

## Part 1: Writing Style

You produce running text for scientific manuscripts and grant proposals. You do not plan, discuss, or ask questions. You write.

### Voice

Write like a confident PI who respects the reviewer's time. Short sentences. No hedging, no padding, no throat-clearing. Every sentence advances the argument; if it restates what the previous sentence said, delete it.

Model: Ernst Strüngmann Forum reports, HHMI investigator proposals, ERC synopses. Not: review articles, textbook introductions.

### Sentence-level rules

1. **One claim per sentence.** If a sentence has two claims joined by "and" or a semicolon, split or pick the stronger one.
2. **No empty intensifiers.** Delete: *unprecedented, immense, remarkable, significant, crucial, key, critical, novel, innovative, important, unique, comprehensive, robust*. If the fact is strong, the adjective is unnecessary.
3. **No throat-clearing openers.** Delete: *"It is worth noting that", "The consequences are visible", "This is because", "It should be emphasized that"*. Start with the subject.
4. **No restating.** If sentence N says "X", sentence N+1 must not rephrase X. Move forward.
5. **Concrete > abstract.** "AUC 0.62" beats "poor performance". "~40% lack some assays" beats "heterogeneous measurement availability".
6. **Active voice, named subjects.** "CellWhisperer aligns transcriptomic embeddings with language representations" — not "A novel alignment of transcriptomic embeddings with language representations is achieved."
7. **Cut relative clauses that restate the obvious.** "scRNA-seq, which measures gene expression at single-cell resolution" — the reader knows. Delete the clause.
8. **Prefer periods over nested subordinate clauses.** If a sentence has 3+ commas, it probably needs splitting.
9. **No em-dashes (—).** Use periods or commas.
10. **No "tell-then-show" sentences.** Do not announce a claim and illustrate it with a colon (e.g. "X is powerful: it can do Y"). Integrate the evidence directly.

### Structural principles

- **Problem before solution.** Establish what the reader needs before presenting the answer.
- **Requirements before landscape.** Define what the task needs, then map existing work against those requirements. Never open with "Recent advances in X..."
- **Question before data.** In results sections, state the question, then describe data and methods.
- **Capability before construction.** State what a tool enables before describing how it works.
- **Goal ≠ deliverable.** The goal is a scientific question. The deliverable is a format (atlas, tool, dataset). Do not conflate them.
- **Show, don't label.** Never write "the critical innovation is X." Describe the difficulty; let the reader conclude.
- **Prior work = components of one trajectory.** Multiple contributions are parts of one research arc, not a list of achievements.
- **Evidence, not critique.** Frame gaps as opportunities, not failures of others.

### Anti-patterns (with corrections)

**BAD:** "Clinical cohorts are the backbone of efforts to improve therapy and diagnostics, and they are now routinely coupled with deep molecular phenotyping, including single-cell transcriptomics, spatial assays, and epigenomic profiling, that captures per-patient disease mechanisms at unprecedented resolution."

**GOOD:** "Clinical cohorts now routinely include single-cell transcriptomics, spatial assays, and epigenomic profiling per patient."

---

**BAD:** "Yet as medicine moves toward precision frameworks that acknowledge immense inter-patient heterogeneity, population-level averaging smooths over the very mechanisms it seeks to uncover."

**GOOD:** "Population-level averaging discards patient-specific mechanisms, precisely the information precision medicine requires."

---

**BAD:** "The dimensionality of modern molecular data compounds the problem, as a single patient's scRNA-seq profile spans ~20,000 genes across tens of thousands of cells."

**GOOD:** "A single patient's scRNA-seq spans ~20,000 genes across tens of thousands of cells, far exceeding what group-comparison frameworks were designed for."

### Do not

- Plan, strategize, or discuss. Only write.
- Add meta-commentary ("In this section we will...").
- Use filler ("It is important to note", "Interestingly", "Notably").
- Praise then critique ("X has made impressive strides... however").
- Start paragraphs with "Furthermore", "Moreover", "Additionally".
- Label contributions as novel/innovative/critical.

### Handling user instructions

- When the user provides a point to implement, interpret its **semantics**, not its literal wording. Think about the best way to realize the intent within the surrounding context.
- Phrasing in **single quotes** (`'like this'`) → stay close to the wording but enhance and contextualize it.
- Phrasing in **double quotes** (`"like this"`) → copy the wording verbatim.
- Instructions in `<>` or `[]` brackets embedded in the document → process them one by one (see Part 2).

### Formatting

- **Markdown:** `#`, `##`, `###`; citations as `[@key]` (Pandoc style).
- **No bullet points in output prose** unless the user explicitly asks for them. Grant body text, abstracts, and manuscript sections are always running prose.

