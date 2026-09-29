# Development Cycle

This repo uses an acceptance-criteria-first workflow.

The lifecycle makes recurring programming checkpoints and their settled context reusable across phases and sessions. This reduces process reconstruction and avoidable rework without weakening authorization, review, verification, or release gates.

## AC Workflow

- Apply `AGENTS.md` `### Four-Phase Workflow` to every phase of this cycle.
- Apply `AGENTS.md` `### Phase-Advancement Rules` to every action instruction and release batch.
- Apply `AGENTS.md` `### Audit Adoption` to every integrated audit cycle.

## Required Artifacts

- `AGENTS.md`
- `README.md`
- `arch.md`
- `plan.md`
- `govna/`

## Cycle

1. **Draft.** Write the authorized AC from `govna/ac-template.md`.
2. **Audit.**
   - Review the AC for missing scope, unsafe assumptions, and untestable requirements without editing it.
   - Start this review immediately when an explicit agent-mediated `govna audit` request emits or reuses one guarded adoption AC.
3. **Refine.**
   - Update a hand-authored AC with settled findings and Director decisions.
   - Keep an audit-emitted AC unchanged.
   - Record its resolved decisions in the active session.
4. **Implement.**
   - Deliver the settled scope.
   - Test the settled scope.
   - Verify the settled scope.
   - Correct implementation defects.
   - Map every scoped path and test in the final read-only closure audit.
5. **Ratify.**
   - Perform the Director-triggered final review.
   - Reuse the Implement evidence snapshot when `AGENTS.md` defines it as current.
   - Revalidate affected evidence when `AGENTS.md` defines it as missing or stale.
   - Apply bounded correction behavior.
6. **Package.** Run `govna/build-release.md` release preparation for the established Ratified release batch or direct batch only after separate Director authorization.

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

Apply the complete phase, scope, correction, contract-integrity, and advancement rules in `AGENTS.md` throughout this cycle.

## Notes

- Keep roadmap decisions and follow-on `IE<N>:` items in `plan.md`.
- Keep architecture in `arch.md`.
- Keep repo governance in `AGENTS.md`.
- Remove an IE when rejected, retired, or shipped through its AC pointer.
- Keep ACs in `govna/ac<N>-<slug>.md`.
- Summarize ACs rather than reproduce them in chat.
- Mark an unscoped stub in `## Summary`.
- Keep an unscoped stub's scope and tests TBD.
- Leave an unscoped stub `PENDING` until scoped.
