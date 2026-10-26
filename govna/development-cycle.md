# Development Cycle

This repo uses an acceptance-criteria-first workflow.

The lifecycle makes recurring programming checkpoints and their settled context reusable across phases and sessions. This reduces process reconstruction and avoidable rework without weakening authorization, review, verification, or release gates.

## AC Workflow

- Apply `AGENTS.md` `### Four-Phase Workflow`, `### Phase-Advancement Rules`, and `### Audit Adoption` to this cycle.

## Required Artifacts

- `AGENTS.md`
- `README.md`
- `arch.md`
- `plan.md`
- `govna/`

## Cycle

### Implement Evidence Snapshot

- Define the Implement evidence snapshot as the session-only identity record used to prove that validation evidence remains unchanged.
- Record the active AC content identity and acceptance-requirement identity.
- Record the primary repository's HEAD, index, tracked-worktree, untracked-path, and untracked-content identities.
- Record each relevant ignored-path and external-input identity.
- Use exact values or deterministic content digests for every recorded identity.
- Record exact validation commands, parameters, working directories, and relevant environment or configuration inputs.
- Record every validation result and acceptance-test disposition without changing its status.
- Record each resolved tool path, executable identity, tool version, and canon identity.
- Use the complete primary-repository state as the default dependency boundary.
- Narrow the dependency boundary only when repository evidence proves that a reused check cannot read the excluded state.
- Treat the snapshot as incomplete when a relevant ignored or external input cannot be identified.
- Capture the snapshot after final validation and the last repository mutation.
- Check the Implement evidence snapshot with non-mutating state, version, content-identity, and diff checks.

### Closure Audit

- Apply this section to the final read-only closure audit that ends Implement under `AGENTS.md` `### Four-Phase Workflow`.
- Map every in-scope entry point, external read, durable write, fallback, gate, and reconciliation path in the closure audit.
- Check every in-scope governance instruction against `AGENTS.md` `## Instruction Style` during the closure audit.
- Map every referenced governance document across applicable source, template, and rendered-consumer paths in the closure audit.
- Compare every discovered path with the active AC `## In Scope`, `## Out Of Scope`, and `## Acceptance Tests` sections.
- Record `Not applicable` with repository evidence when a path category is absent.
- Record every acceptance-test disposition and residual risk in the closure audit.

## Notes

- Keep roadmap decisions and follow-on `IE<N>:` items in `plan.md`.
- Keep architecture in `arch.md`.
- Remove an IE when rejected, retired, or shipped through its AC pointer.
- Keep ACs in `govna/ac<N>-<slug>.md`.
- Mark an unscoped stub in `## Summary`.
- Keep an unscoped stub's scope and tests TBD.
- Leave an unscoped stub `PENDING` until scoped.
