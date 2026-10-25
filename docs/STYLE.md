# Docs style guide

This guide gives one template for each page type, the limits for a page, and the rule for evidence marks. When you write or change a page under `docs/`, use it. Each page opens with a summary and puts the common case before the rare one.

## Page types

Each page has one type. The type sets the order of the sections.

| Type | Reader's question | Example |
|---|---|---|
| Tutorial | How do I get a first result? | [Worked client sessions](EXAMPLES.md) |
| How-to | How do I do this task? | [Running the daemon](DAEMON.md) |
| Reference | What exactly does this do? | [git.status](protocol/git-status.md) |
| Explanation | Why does it work this way? | [Architecture](ARCHITECTURE.md) |
| Record | What happened, and when? | [Reference builds](REFERENCE-BUILDS.md) |

Do not mix types on one page. Link to the explanation instead of explaining in a how-to.

## Templates

"Top line" below means the line of [Evidence marks](#evidence-marks).

### Reference: a method page

```markdown
# method.name

Summary in 2 or 3 sentences. Say what the method does, when a client calls it, and its one surprise.

An untagged rule on this page matches build `<id>` or an earlier build, measured on Linux, macOS and Windows.

## Request
Example frame, then a table of parameters.

## Response
Example frame, then a table of members.

## Errors
One table: code, text, cause. Link to a shared rule, do not copy it.

## Behavior
The common case first, in the order a client meets it.

## Platform notes
=== "Linux"
    Text for Linux.
=== "macOS"
    Text for macOS.
=== "Windows"
    Text for Windows.

## Rare states
??? note "When the git directory is a link"
    The rule, in a few sentences.

## Differences from the reference
Link to each divergence entry.
```

A page keeps a heading that other pages link to, in the template order. Use one tab for systems that share a rule. A tag inside a tab narrows that tab, and an untagged rule in a tab follows the top line. Group an Errors table of more than about 8 rows under H3 headings.

### How-to

```markdown
# Task name, as a verb phrase

Summary: the goal and the result, in 2 sentences.

Top line.

## Before you start
What the reader needs: a version, a file, a right.

## Steps
1. Imperative step. Condition before command.
2. Next step, with the command in a code block.

## Check the result
The output that proves it worked.

## If it fails
One row for each known failure: the symptom and the fix.
```

### Tutorial

```markdown
# What you will build

Summary: the end result and the time it takes.

Top line.

## Before you start
## Step 1: <name>
Each step ends with something the reader sees.
## What you learned
Links to the how-to and reference pages.
```

### Explanation

```markdown
# The thing, as a noun phrase

Summary: the question this page answers.

Top line.

## The idea
## How it works
A diagram or an example before the rules.
## Why this way
The cost and the alternative that lost.
## Related
```

### Record

```markdown
# Name of the record

Summary: what the record holds and who keeps it.

Top line.

## Table
One row for each entry, newest first.

## Entries
One short section for each entry, in the same order.
```

## What goes where

The main text holds what a reader needs in the common case. A rule that the reader acts on stays on the page, with one example for each rule that a client applies.

Put a rare state in a collapsed block (`??? note "Title"`). A rare state is a state a client reaches only in an unusual setup, such as a link in a path. A collapsed block still counts for the word limit.

Move these out of the page:

- History of builds, and "before, claustrum did X": to [the shipped ledger](IMPROVEMENTS.md) or [the reference builds](REFERENCE-BUILDS.md).
- Cell names, round names, run counts, VM names and tags, and private tools: out of the repo. This repo serves forks, so name no tool that a fork does not have.

A fact tagged "not measured" or "from the code" stays on the page. It tells the reader how far to trust the rule.

Each fact has one home. Link to the home. Do not copy its text. A rewrite can move a fact to its shared home, and the fact map names that home.

## Limits

**Words between two headings: at most 250.** This is a target for new and rewritten text. No test enforces it yet. A table or a tab block counts as one unit.

**Sentence length: 8 to 20 words in a procedure, 8 to 25 words in a description.** A run of sentences under 8 words reads as a list with no thread. Join two related short sentences, and say why before the rule.

**Words in a page.** Every page except `docs/REFERENCE-BUILDS.md` has a word limit. A row in `docWordLimits` (`docs_consistency_test.go`) sets it, or the default applies: 1500 words, or 1200 under `docs/protocol/`. A new page gets its count plus about 200 words. Do not raise an existing limit: cut instead.

Keep these plain-English rules:

- Write imperative steps: "Send the token", not "The token is sent".
- Use simple tenses. Use present tense for what the daemon does.
- Write one idea in a sentence. Do not use semicolons or em-dashes.
- Put the condition before the command: "If the file is gone, send `offset` again."
- Do not hedge with modal verbs. Say what happens, or say "can" with the condition.
- Explain a term at first use, or link to the [glossary](GLOSSARY.md).

## Evidence marks

Evidence is the proof for a rule: which build and which system it was measured on. Do not repeat it after every sentence, and never drop a fact to save a tag.

1. **A top line on every page.** It is the first line after the summary, in one fixed form: "An untagged rule on this page matches build `<id>` or an earlier build, measured on Linux, macOS and Windows."
2. **A tag for each exception.** Put the tag at the end of the block that it covers. The tags are "(measured on Linux)" (or macOS or Windows, or two of them), "(not measured)", "(from the code)" and "(claustrum's own)". The tag names the system that ran the test and claims no other. A tag copies the evidence of the old page for that rule, and never widens or narrows it. Two tags on one rule sit side by side, "claustrum's own" first. "Not measured" says what is unmeasured: if it is the reference side, the sentence says so. Every rule of those kinds carries its tag. The top line of the page explains the tags.
3. **A scope sentence for a section or a subsection.** If all rules of it share one tag, use one. Say it once under the heading and leave the tags out inside. A mixed section keeps its tags. A tag covers only its own block. If one block holds two rules with different evidence, each rule keeps its tag.

Before (the old text of `docs/protocol/git-status.md`, before this rewrite):

```markdown
claustrum's own, not measured: a cut can split a multi-byte character. In a porcelain line, the JSON encoding then sends U+FFFD for each leftover byte.
```

After:

```markdown
A cut can split a multi-byte character (claustrum's own) (not measured). In a porcelain line, the JSON encoding then sends U+FFFD for each leftover byte (claustrum's own) (not measured).
```

Each rule keeps its own tags, because they are separate rules.
