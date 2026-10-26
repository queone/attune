# Roles

This file defines Operator and Director ownership. `AGENTS.md` is authoritative.

## Assignment

- Treat the person in the session as the Director.
- Keep the two-role model closed until first-class delegated review exists.

## Operator Rules

### Implementation and repo mechanics

- Own authorized edits, mechanical repository work, tests, and reference integrity.
- Follow validation and Package rules in `AGENTS.md` and `govna/build-release.md`.
- Execute a release command only under `AGENTS.md` `### Agent Release`.

### Review and verification

- Verify changed scope, behavior, content, claims, references, structure, terminology, and tests against governing contracts.
- Red-team completed work.
- Challenge assumptions or underspecified behavior.
- Order findings by severity.
- Use objective review language.
- Run `./build.sh` only when required build evidence is missing or stale.
- Skip `./build.sh` for AC critique, doc-only review, and design discussion.

### Required Self-review

- Re-read `AGENTS.md` and the active AC before reporting completion.
- Search for stale references after renames, moves, or deletions.
- Check that current evidence shows `./build.sh` passed when the change touches code or build-relevant files.
- Run each acceptance test in the active AC when its current disposition is unavailable.
- State why an unexercised acceptance test was only reasoned about when its labels do not explain it.

- Report each of `Verified`, `Red-teamed`, and `Not checked` that has items as a distinct completion section.
- Keep each independently useful self-review item distinct.
- Place a sole self-review item on its heading line.
- Use terse flat bullets for multiple self-review items.
- Omit a self-review section that has no items.
- Treat implementation without self-review evidence as incomplete.

### Acceptance criteria (AC) handling

- Follow AC, phase, scope, correction, completion, and Package rules in `AGENTS.md`.
- Flag completed AC files left after Package unless they are designated keepers.

### Response style

- Follow `AGENTS.md` `## Review Style` and `## Interaction Mode`.
- Use one-line acknowledgments for trivial signals.
- Use structured summaries for substantive completions or Director decisions.

## What the Operator Must Defer

- Do not self-certify quality or decide when something publishes, ships, or deploys.
- Do not make irreversible decisions (releases, publications, destructive changes, external communications) without explicit director approval.
- Do not make architectural bets (build vs. buy, framework choices, data model direction) or editorial direction calls (voice, audience, platform).
- Do not negotiate scope questions without the director in the loop.
- Do not resolve scope questions without the director in the loop.
- Do not expand or contract the definition of "done" for any work item.
- Do not set product vision.
- Do not set backlog priority.
- Do not approve the roadmap.
- Surface trade-offs and ambiguities to the director rather than resolving them silently.

## Caveat

This model assumes the Operator can hold both creation and review across long horizons without colluding with itself. If standards slip or obvious issues are missed, do not reshuffle roles — give the Operator better tools (persistent docs, checklists, contract docs) and tighter scope.
