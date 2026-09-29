# attune Architecture

## Purpose

attune keeps Azure DNS, security groups, app registrations, role definitions, role assignments, and resource groups matching provider-neutral YAML specs, with the live provider as the only source of truth.

## System Summary

attune reads specs and `attune.yaml` from its encrypted store and validates them offline. For `plan` and `apply` it reads live Azure Resource Manager and Microsoft Graph state, prints the changes, and applies the reviewed ones.

## Current Platform

- Go

## Major Components

- `cmd/attune/main.go`: help, flags, and the validate, plan, and apply flow
- `spec.go`, `config.go`, `model.go`: spec parsing, configuration precedence, and the resource model
- `reconcile.go`: change planning per resource type
- `azure.go`: the ARM and Graph provider
- `store.go`, `key.go`, `edit.go`, `render.go`: the store, its key, `edit`, and `render` and `cat`
- `github.com/queone/gkit/lockbox`: the encrypted store and key store

## Core Files

- `AGENTS.md`: base governance contract
- `plan.md`: prioritized roadmap and approved direction
- `build.sh`: self-contained build / release-prep / release script (Bash 3.2+, no external tools)
- `govna/development-cycle.md`: workflow from roadmap through release
- `govna/ac-template.md`: acceptance-criteria template for new work
- `govna/build-release.md`: build, test, and release rules

## Data And Control Flow

attune resolves the store, opens it with the Keychain key, loads `attune.yaml` and the specs, and validates them offline. For `plan` and `apply` it then authenticates, reads live state, plans the changes, and prints them; `apply` makes them one at a time, printing each as it lands.

## AC Lifecycle Control Flow

The governed change path is `Draft → Audit → Refine → Implement → Ratify → Package`. Draft creates the AC; Audit, Refine, Implement, and Ratify are the four AC phases; Package is post-Ratify release preparation and is not a fifth phase.

Integrated audit adoption is the only command-mediated phase exception. It can advance one emitted adoption AC through immediate Audit and no-edit Refine, but it cannot enter Implement. Every unpackaged AC with implementation in the unreleased state enters the pending release batch, including work awaiting Ratify. A private pre-Implement calculation prevents that complete batch from growing beyond one 80-byte prefix-plus-summary message. Package requires every member to be Ratified, rejects excluded implemented work, and rechecks the complete batch before prep. A named request such as `Package AC70+AC71` establishes a fitting multi-AC batch; a standalone Package alias reuses the complete batch already established in the active session.

## Architecture Notes

- record stable system decisions here
- prefer durable structure and interfaces over transient implementation detail
- attune authenticates through the local `az` CLI and never keeps its own credential store.
- It writes no local state, cache, or telemetry beyond its store, the remembered store path, `render` output, and `edit`'s private temporary file.
- Requests are restricted to an ARM and Graph origin allowlist, and non-2xx response bodies are redacted wholesale before any diagnostic or error message.
- Role-assignment and role-definition IDs are derived deterministically (SHA1-based UUIDv5), to stay stable across repeated `apply` runs and compatible with resources that rkit's attune created.
- The store is lockbox's sealed SQLite file with its own key under the `attune` Keychain service. Its file starts with the `MACFIT` format tag, which must not change.
- DNS pruning defaults on. Identity, role, and resource-group pruning default off.
- lockbox comes from `github.com/queone/gkit` at a pinned version. Raise that version deliberately.

## Conventions

- update this document when architecture or major workflow changes materially
- keep implementation detail in code and stable architecture here
