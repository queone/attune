# attune

A small declarative reconciler. Describe cloud resources as provider-neutral YAML specs, keep the specs in an encrypted
store, and let attune compare them with what the provider actually holds. `azure` holds DNS, security groups, app
registrations, role definitions, role assignments, and resource groups. `aws` holds Route 53 DNS, a host of IAM role,
bucket, firewall, instance, and static address, a cost budget, and an uptime check. The live provider remains the source of
truth; there is no state backend, initialization step, or local state file.

## Why

Infrastructure tools keep a state file, and the state file is where drift hides and secrets leak. attune keeps none. It
reads YAML specs from an encrypted store, compares them with what the provider actually holds, and applies only the reviewed
difference. Credentials stay with each provider's own CLI, and attune writes nothing else to disk.

## Getting Started

Install the latest release:

```bash
go install github.com/queone/attune/cmd/attune@latest
```

Or clone this repository and run `./build.sh`, which tests, builds, and installs `attune`.

Then create a store, add the configuration and specs, and validate them offline:

```text
attune init -N -t ~/Sync/attune.store       # create the store and its key, remember the path
attune add attune.yaml ./attune.yaml        # store the configuration under its name
attune add ./specs                          # import every spec under a directory
attune validate                             # validate specs offline
attune plan                                 # read live state and show changes
attune apply                                # create, update, and permitted prune operations
```

See the [synthetic example](examples/) for a complete set of specs. Validation is offline; planning and applying require
authentication and deliberate replacement and review of every documented placeholder.

## Usage

Run `attune -h` for the full help, including commands, options, and the store path order.

## Documentation

- [docs/commands.md](docs/commands.md): what `validate`, `plan`, and `apply` do, configuration precedence, how a spec picks
  its provider, references between specs, what a live run prints, and what attune writes to disk.
- [docs/azure.md](docs/azure.md): the `azure` provider, its kinds, and their rules.
- [docs/aws.md](docs/aws.md): the `aws` provider, the account shape it expects, the host and monitoring kinds, and the
  region guardrail edit a health check needs.
- [docs/store.md](docs/store.md): the encrypted store, where it lives, and how to work with it across Macs.
- [arch.md](arch.md): the architecture and its stable decisions.

## History

attune lived in [gkit](https://github.com/queone/gkit) through gkit v0.87.0 (attune v1.6.0), then moved to this repository.
Its encrypted store code is gkit's `lockbox` package, which attune imports.
