# Commands and behavior

What every attune command does, how a spec picks its provider, what a live run prints, and what attune leaves on disk.
Provider-specific rules live in [azure.md](azure.md) and [aws.md](aws.md); the encrypted store in [store.md](store.md).

## The three commands

```text
attune validate              # validate specs offline
attune plan                  # read live state and show changes
attune apply                 # create, update, and permitted prune operations
```

`validate` parses and checks specifications without credentials or provider access. `plan` authenticates and reads live
state but does not mutate it. `apply` performs the reviewed create and update actions plus only those deletes enabled by the
applicable prune policy. Every live run prints non-secret provider grounding so the operator can confirm its target.

Live commands require the provider's CLI signed in: `az login` for `azure`, and for `aws` a signed-in AWS CLI profile,
`aws login --profile <name>` under an organization that signs in through Identity Center, or `aws configure` with an access
key otherwise.

Run `attune -h` for the full help, including commands, options, and the store path order.

## Configuration

Configuration is the store's `attune.yaml` entry; precedence is flag, environment (`ARM_SUBSCRIPTION_ID`,
`ARM_RESOURCE_GROUP`), configuration file, then built-in default. `attune.yaml` accepts one block per registered provider:
`azure` with `subscription` and `resource_group`, and `aws` with optional `region` and `profile`, the CLI's own defaults
otherwise.

`attune.yaml` may reference environment variables as `$VAR` or `${VAR}`. A reference to an unset variable stops every
command that reads the configuration, naming the variable, rather than expanding to nothing: an empty `subscription` would
fall back to whatever subscription `az login` last chose. A variable set to the empty string expands to it, and full-line
comments are never expanded.

DNS pruning defaults to enabled. Identity, role, resource-group, host, and monitoring pruning default to disabled and must be
enabled explicitly with their corresponding flags or configuration.

An optional `content_version` string in `attune.yaml` declares the content version of the stored specs; when set, attune
appends ` content=<value>` to the validate result and to the live grounding line. The convention decouples spec content from
repository tags: bump `content_version` when provider-affecting spec content changes, and leave it (and any git tag) alone
for unrelated repository changes — attune never reads git.

## Providers

Every spec may carry a `provider` field naming the cloud it belongs to. A spec without one uses the default provider: `-P`,
then `attune.yaml` `provider`, then `azure`. Provider names resolve through a registry that lists each provider's supported
kinds, so `validate`, `add`, and `edit` reject an unknown provider, or a kind that provider cannot hold, offline and before
any credential is read. Two providers are registered: `azure` supports every kind, and `aws` supports `dnsRecordSet` and the
host and monitoring kinds.

A live run groups the specs by provider, in name order except that a provider holding a spec another provider references
comes before it: it grounds and plans every provider first, printing one grounding line
and one `attune plan: provider=<name>` block each, and only then applies, one `attune apply: provider=<name>` block each, so
a provider that cannot be reached stops the run before any other provider is changed. With one provider in the store the
output is unchanged. A provider that receives a secret through `deliverTo` is grounded before any provider plans, so an
unreachable delivery target stops the run before any plan.

## References between specs

A `dnsRecordSet` value can reference another spec's live output instead of holding a literal:
`${staticAddress:<name>:publicIp}` or `${instance:<name>:id}`, the whole value and nothing else. `validate` checks each
reference offline: its form, that the kind has that readable field, and that the named spec exists on exactly one
provider. A reference may cross providers, such as an `azure` record holding an `aws` address: the provider holding the
address plans and applies first, and every provider shares the outputs it learns. A kind and name held on two providers,
or providers that reference each other, are refused, since no single spec or apply order would satisfy them. At plan time
a reference to a resource that already exists resolves from live state, so a matching record shows no drift, and `-V`
shows the resolved value. A reference to a resource the same run creates plans the record as `awaits ${...}`, and
`apply` fills it after the host change lands, since host kinds, and providers holding referenced specs, run first. A record whose reference cannot be filled fails `apply` before any DNS call. A value that opens
`${` must be a reference; there is no escape for the literal text. A `healthCheck` `target` accepts the same references.

## What a live run prints

The change block a live run prints is prospective: change lines and the `N change(s) would be made.` trailer render in
yellow, under an uncolored `attune plan: provider=<name>` header, for both `plan` and `apply`. `apply` then prints a green
confirmation block headed `attune apply: provider=<name>` — one green past-tense line (`created`/`updated`/`deleted`) per
change, printed as that change lands, so an interrupted run shows exactly what was applied — followed by a green
`N change(s) made.` trailer on full success. Color is suppressed automatically for non-TTY output, `NO_COLOR`, and
`TERM=dumb`.

DNS zone creation is part of the plan: a spec referencing a zone that does not exist plans as `+ create dnsZone|<zone>` ahead
of that zone's record changes, and `apply` creates it through the same reviewed change loop. Existing zones are never
rewritten.

Normal plans print resource keys and concise summaries, but omit DNS values, tag values, memberships, owners, role actions,
credentials, and provider response bodies. `-d`/`--diagnostic` adds non-secret account and target grounding.
`-V`/`--verbose` opts in to field-level detail: each planned update gains indented `field: old -> new` lines (added/removed
entries for set-valued fields) showing exactly which values drive it — including live tenant values the default output
deliberately omits.

## What attune writes

Apart from its store, the remembered store path, `render` output, and the private temporary file `edit` opens, attune writes
no local state, cache, telemetry, copied specs, or diagnostic artifacts; serviced-repository data is sent only to the
configured provider endpoints during an operator-requested live command.
