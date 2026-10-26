# attune Architecture

## Purpose

attune keeps Azure DNS, security groups, app registrations, role definitions, role assignments, and resource groups matching provider-neutral YAML specs, with the live provider as the only source of truth.

## System Summary

attune reads specs and `attune.yaml` from its encrypted store and validates them offline. For `plan` and `apply` it groups the specs by provider, reads each provider's live state (Azure Resource Manager and Microsoft Graph for `azure`, Route 53 through the `aws` CLI for `aws`), prints the changes, and applies the reviewed ones.

## Current Platform

- Go

## Major Components

- `cmd/attune/main.go`: help, flags, and the validate, plan, and apply flow
- `spec.go`, `config.go`, `model.go`: spec parsing, configuration precedence, and the resource model
- `reconcile.go`: change planning per resource type
- `provider.go`: the provider registry and the capability interfaces (`DNSProvider`, `IdentityProvider`, `RoleProvider`, `ResourceGroupProvider`, `IamRoleProvider`, `StorageProvider`, `NetworkProvider`, `ComputeProvider`) a provider implements
- `azure.go`: the ARM and Graph provider
- `aws.go`: the Route 53 and host provider over the `aws` CLI
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
- Specs pick their provider by name; the registry owns each provider's name, kinds, `attune.yaml` block, and constructor, so adding a provider touches only its own file plus one registry entry.
- Each provider owns its credential source and allowlist. The Azure provider authenticates through the local `az` CLI and never keeps its own credential store. The AWS provider drives the local `aws` CLI as its only credential source and transport, through an exact allowlist of `<service> <operation>` pairs, and reduces a failing call to its command and exit status.
- It writes no local state, cache, or telemetry beyond its store, the remembered store path, `render` output, and `edit`'s private temporary file.
- The Azure provider restricts requests to an ARM and Graph origin allowlist, and non-2xx response bodies are redacted wholesale before any diagnostic or error message.
- Role-assignment and role-definition IDs are derived deterministically (SHA1-based UUIDv5), to stay stable across repeated `apply` runs and compatible with resources that rkit's attune created.
- The store is lockbox's sealed SQLite file with its own key under the `attune` Keychain service. Its file starts with the `MACFIT` format tag, which must not change.
- DNS pruning defaults on. Identity, role, resource-group, and host pruning default off.
- Host kinds plan in dependency order (`iamRole`, `bucket`, `firewall`, `instance`, `staticAddress`, then DNS) and prune in reverse. An immutable-field change plans as `! replace` and apply refuses it.
- The AWS provider tags everything it creates `attune=managed` and lists only tagged resources, so prune is bounded to attune's own resources.
- lockbox comes from `github.com/queone/gkit` at a pinned version. Raise that version deliberately.

## Conventions

- update this document when architecture or major workflow changes materially
- keep implementation detail in code and stable architecture here
