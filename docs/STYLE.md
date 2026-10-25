# Docs style guide

This guide gives one template for each page type, the limits for a page, and the rule for evidence marks. When you write or change a page under `docs/`, use it. A reader needs to find the answer fast, so each page opens with a summary and puts the common case before the rare one.

## Page types

Each page has one type. The type sets the order of the sections. The site map names the type of every page.

| Type | Reader's question | Example |
|---|---|---|
| Tutorial | How do I get a first result? | A tour of one session |
| How-to | How do I do this task? | Run the daemon |
| Reference | What exactly does this do? | A method page |
| Explanation | Why does it work this way? | Architecture |
| Record | What happened, and when? | Reference builds |

Do not mix types on one page. A how-to that stops to explain a design loses the reader. Link to the explanation instead.

## Templates

### Reference: a method page

```markdown
# method.name

Summary in 2 or 3 sentences. Say what the method does, when a client calls it, and its one surprise.

An unmarked rule on this page matches build `<id>`.

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

### How-to

```markdown
# Task name, as a verb phrase

Summary: the goal and the result, in 2 sentences.

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

## Table
One row for each entry, newest first.

## Entries
One short section for each entry, in the same order.
```

## What goes where

The main text holds what a reader needs in the common case. A rule that the reader acts on stays on the page, with one example for each rule that a client applies.

Put a rare state in a collapsed block (`??? note "Title"`). A rare state is a rule that applies to one input in a thousand, such as a link in a path. A reader who does not meet it skips it. A collapsed block still counts for the word limit.

Move these out of the page:

- History of builds, and "before, claustrum did X".
- Measurements, run counts, cell names and round names.
- VM names and tags.
- Private tools and lab notes. This repo serves forks, so name no tool that a fork does not have.

Each fact has one home. Link to the home. Do not copy its text.

## Limits

**Words between two headings: at most 250.** The short method pages of the baseline run 140 to 250 words under a heading, and a reader finishes them. The long pages run 1,000 words and more under one heading, and a reader loses the thread. If a section grows past 250 words, split it under a new heading or move a rare part to a collapsed block.

**Sentence length: 8 to 20 words in a procedure, 8 to 25 words in a description.** The site average today is 9 to 14 words. A run of sentences under 8 words reads as a list with no thread. Join two related short sentences, and say why before the rule.

Keep these plain-English rules:

- Write imperative steps: "Send the token", not "The token is sent".
- Use simple tenses. Use present tense for what the daemon does.
- Write one idea in a sentence. Do not use semicolons or em-dashes.
- Put the condition before the command: "If the file is gone, send `offset` again."
- Do not hedge with modal verbs. Say what happens, or say "can" with the condition.
- Explain a term at first use, or link to the [glossary](GLOSSARY.md).

## Evidence marks

Evidence is the proof for a rule: which build and which system it was measured on. Do not repeat it after every sentence. Use three tools.

1. **One top line.** Each page opens with one sentence. It names the build that the unmarked rules match, for example "An unmarked rule on this page matches build `5fd08069` or an earlier build, as measured on Linux, macOS and Windows."
2. **A short tag for an exception.** Put the tag at the end of the block that it covers, in plain words: "(Linux only)", "(own, unmeasured)" or "(from the code)". Give each tag hover text with an abbreviation line, such as `*[Linux only]: Measured on Linux alone.`
3. **One scope sentence for a whole section.** Say so once under the heading, when every rule in a section has the same class of evidence. Then add no tags inside it.

Before:

```markdown
One entry for each gitlink in the index, unless `<name>/.git` does not exist (measured on Linux). There is one entry for each index stage (measured on Linux).
```

After:

```markdown
One entry for each gitlink in the index, unless `<name>/.git` does not exist. There is one entry for each index stage. (Linux only)
```

The tag covers the block. A reader sees the rule first and the limit of the proof second.
