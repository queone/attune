# Build and Release

## Build and Test Rules

- Keep one documented canonical build command.
- Route formatting, checks, tests, and packaging through it.
- Keep release work out of routine implementation.

Use self-contained `build.sh` for build, release prep, and release work without external govna tools.

### Build Presentation

- Reuse the canonical build color policy and palette across supported CODE stacks.
- Color phase headings, command previews, status values, failures, prep output, and release output by semantic role.
- Emit plain output when stdout is not a terminal.
- Emit plain output when `NO_COLOR` is set.
- Emit plain output when `TERM=dumb`.
- Require a 256-color-capable terminal before emitting ANSI sequences.
- Preserve plain-text content and output streams when color is disabled.
- Keep self-contained build scripts compatible with Bash 3.2.

## Minimum Validation

- Require formatting, static checks, automated tests, and behavior-aligned docs to pass.



## Canonical Build Commands

```bash
./build.sh
```

To scope the run to selected commands:

```bash
./build.sh <target> [<target> ...]
```

Use space-separated target names. Supported CODE stacks may retain package-wide shared-code validation while limiting target-specific checks, tests, artifacts, and installation to the selected targets.

Run `./build.sh` without targets for repository-wide validation. Follow the applicable stack guidance above for release-prep evidence, pre-change validation, and build-state reuse. Release-prep validation uses the package-wide form.

## Independent Utility Versions

- Treat the repository/package version as the version input and release metadata governed by the existing release mechanism.
- Require one normalized record for each installable utility with its canonical target name, declaration location, declared version, and `--version` invocation.
- Accept only `^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$` as a strict stable SemVer declaration.
- Require `--version` to exit 0.
- Require `--version` to print exactly `<utility-id> v<MAJOR.MINOR.PATCH>` plus its newline to stdout.
- Require `--version` to write nothing to stderr.
- Validate every declaration before compilation.
- Validate each compiled utility result before installing that utility.
- Validate every compiled utility result before release-metadata writes.
- Reject missing, empty, malformed, duplicate, orphaned, and mis-mapped records with a non-zero error that names the utility and recovery action.
- Preserve all independent utility declarations and outputs during repository release prep.

## Pre-Release Checklist

- Apply this checklist only to an explicit Director Package instruction for an established Ratified release batch or direct batch.
- Apply the Package gates in `AGENTS.md` `### Four-Phase Workflow` and `### Phase-Advancement Rules` before prep.

Note: the operator flow has two steps.

1. **Run prep.**
   - Classify the AC scope under semver.
   - Draft a release message that names the delivered user-visible result and every established release-batch AC reference in no more than 80 bytes.
   - Name each direct-handled companion change inside that user-visible result without an AC reference.
   - Keep the release message on one line and inside one Markdown table cell.
   - Exclude every AC reference outside the established release batch.
   - Use the successful final full build and clean Ratify review as current Package evidence.
   - Use the successful final full build as current Package evidence for a direct batch.
   - Rerun applicable validation before prep when Package evidence is missing or stale.
   - Run the stack-defined `./build.sh prep vX.Y.Z "message"` invocation.
   - Pass current validation evidence with `--validation-token` or `-t` when supported.
   - Keep Go prep free of canonical build, Go build, and Go dependency commands.
   - Reserve validation-token evidence for Rust prep.
   - Refresh validation-token evidence for Rust prep.
   - Use `--dry-run` or `-n` to inspect without writes.

   Before running prep, satisfy this repository's declared version-target contract and keep repository/package and independently versioned utility declarations aligned as required by its Project Practices.
2. **Run the printed release command.**
   - Run `./build.sh vX.Y.Z "message"`.
   - Confirm the displayed candidate files and exact release sequence.
   - Approve the interactive prompt to commit, compile, validate, install, tag, and push.

- End the structured Package completion report with `Run below to release:`.
- Place the exact release command immediately after that line.
- Add nothing after the release command.

### Appendix: what prep does

`./build.sh prep` performs release bookkeeping in every CODE stack. Go and Terraform prep perform bookkeeping only. Rust prep adds a fallback pre-change full build when validation evidence is missing or stale and one post-change full build after its writes. Swift prep runs the canonical build before its writes and again with installation after them unless `-B` skips both. Every stack follows these bookkeeping phases:

1. **Validate inputs.** Require strict stable SemVer and one non-empty, single-line, table-safe message of no more than 80 bytes.
2. **Validate git state.** Inside a git work tree, target tag does not exist yet, HEAD is not at the latest tag with a clean working tree.
3. **Process version targets.**
   - Detect every version target.
   - Validate every version target.
   - Follow this repository's Project Practices.
   - Follow the stack build implementation.
   - Reject missing, malformed, duplicate, or unsafe targets before any write.
   - Reject a bumped utility's README whose `### Usage` block cannot match the help probe.
4. **Guard CHANGELOG shape and idempotency.**
   - Detect the root `CHANGELOG.md` target.
   - Require the canonical heading and two-column table.
   - Reject an existing row for the target version before any write.
5. **Parse AC refs.** Scan the release message for `AC[0-9]+`; require one matching AC file for every unique reference after the Operator verifies exact release-batch equality. Accept a message without AC references.
6. **Apply writes.**
   - Apply idempotent version bumps.
   - Rewrite each bumped utility's README `### Usage` version line.
   - Insert the CHANGELOG row under `| Unreleased | |`.
   - Delete each released AC file whole.
   - Sweep matching AC-pointer IE lines from `plan.md`.
   - Skip writes under `--dry-run` or `-n`.
   - Leave already-swept lines unchanged on rerun.
7. **Verify and report.** Reject results outside the planned paths, verify every planned transformation, and print one shell-safe two-argument release command.

CHANGELOG row shape (enforced by prep's insertion code and by convention):

- Use a `# Changelog` heading.
- Follow it with the two-column `| Version | Summary |` table.
- Use `|---------|---------|` as the separator.
- Keep `| Unreleased | |` as the first data row.
- Add one row per release.
- Keep summaries single-line and no longer than 500 characters.
- Escape each literal pipe in a summary as `\|`.
- Lead summaries with the AC reference when one exists.
- Versions are unprefixed (`0.29.0`, not `v0.29.0`).
- Do not backfill historical tags or invent alternative shapes (Keep-a-Changelog, sectioned `## vX.Y.Z`, etc.).

## Project Practices
