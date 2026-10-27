# attune
A small declarative reconciler: describe provider-neutral intent in YAML and compare it directly with live Azure Resource Manager and Microsoft Graph state. It supports DNS record sets, security groups, app registrations, role definitions, role assignments, and resource groups. DNS values are compared without ordering sensitivity, resource-group tags use merge semantics, and the live provider remains the source of truth — there is no state backend, initialization step, or local state file.

```text
attune validate              # validate specs offline
attune plan                  # read live state and show changes
attune apply                 # create, update, and permitted prune operations
```

See the [synthetic attune example](examples/) for a complete set of six specs. Validation is offline; planning and applying require authentication and deliberate replacement and review of every documented placeholder.

Live commands require the provider's CLI signed in: `az login` for `azure`, `aws configure` for `aws`. Configuration is the store's `attune.yaml` entry; precedence is flag, environment (`ARM_SUBSCRIPTION_ID`, `ARM_RESOURCE_GROUP`), configuration file, then built-in default. DNS pruning defaults to enabled. Identity, role, and resource-group pruning default to disabled and must be enabled explicitly with their corresponding flags or configuration. Role assignments accept `group` and its case-insensitive `securityGroup` alias, `servicePrincipal`, or `user` as `principalType`. A literal directory object ID may omit `principalType`; a named principal must provide it, and `attune validate` enforces this rule offline.

Every spec may carry a `provider` field naming the cloud it belongs to. A spec without one uses the default provider: `-P`, then `attune.yaml` `provider`, then `azure`. Provider names resolve through a registry that lists each provider's supported kinds, so `validate`, `add`, and `edit` reject an unknown provider, or a kind that provider cannot hold, offline and before any credential is read. Two providers are registered. `azure` supports every kind. `aws` supports `dnsRecordSet` on Route 53 through the local `aws` CLI, which is its only credential source and transport: every call is an allowlisted `aws <service> <operation>` invocation with JSON output, and a failing call is reported as its command and exit status with the process output dropped. `attune.yaml` accepts one block per registered provider: `azure` with `subscription` and `resource_group`, and `aws` with optional `region` and `profile`, the CLI's own defaults otherwise. On Route 53 the zone apex is `@` as on Azure, TXT values are written unquoted in specs and quoted on the wire, hosted zones are created implicitly when a record set names one that does not exist, private hosted zones are ignored, and alias record sets are neither managed nor pruned.

The `aws` provider also reconciles a host through five provider-neutral kinds: `iamRole` (managed policy ARNs, inline policy documents by name, and an instance profile of the same name; the trust policy is fixed to EC2), `bucket` (region, a public access block that defaults to fully blocked, and `encryption` of `aes256` or a key ARN), `firewall` (ingress rules of `protocol`, `fromPort`, `toPort`, and `sources` that are CIDRs or other firewall names, in the default VPC unless `vpc` names one by its Name tag), `instance` (`type`, `image` as an AMI id or an SSM parameter path resolved at plan time, `role`, `firewall`, `imdsv2Required` defaulting on, `rootDisk` size, type, and encryption, and tags with merge semantics), and `staticAddress` (attached to an `instance` by name). Plan and apply run them in dependency order, `iamRole`, `bucket`, `firewall`, `instance`, `staticAddress`, then DNS, and prune them in reverse. Every resource attune creates carries the tag `attune=managed`, IAM roles and instance profiles also live under the path `/attune/`, and the provider lists only those, so pruning can never reach a resource attune did not create. Each family is found where it lives: roles by their path, buckets and health checks by listing them and reading their tags, so a bucket in any region is found, and EC2 instances, firewalls, and addresses through the tagging API in the configured region. `us-east-1` is AWS's management region, where IAM, Route 53, STS, and Budgets live; the configured region is where the host runs. A launch that fails right after its instance profile was created is retried for about thirty seconds, since IAM takes a few seconds to make a new profile visible, and an address waits up to a minute for its instance to leave `pending`, since it cannot attach before then. Host pruning is off by default; `prune.hosts: true` or `-H/--prune-hosts` turns it on. A change to an immutable field, an instance's `type`, `image`, or root disk type and encryption, or a bucket's `region`, plans as a `! replace` line and `apply` refuses it, naming the field: remove the spec, apply, then add it back. The applied line for a new `staticAddress` shows the public address.

A `dnsRecordSet` value can reference another spec's live output instead of holding a literal: `${staticAddress:<name>:publicIp}` or `${instance:<name>:id}`, the whole value and nothing else. `validate` checks each reference offline: its form, that the kind has that readable field, and that the named spec exists on the same provider; a reference across providers is rejected and stays a two-pass change. At plan time a reference to a resource that already exists resolves from live state, so a matching record shows no drift, and `-V` shows the resolved value. A reference to a resource the same run creates plans the record as `awaits ${...}`, and `apply` fills it after the host change lands, since host kinds run before DNS. A record whose reference cannot be filled fails `apply` before any DNS call. A value that opens `${` must be a reference; there is no escape for the literal text. A `healthCheck` `target` accepts the same references.

Two monitoring kinds complete the `aws` host. `budget` is a monthly cost budget: `amount` as a decimal, `currency` defaulting to `USD`, and `alerts` of a `threshold` percent, an optional `forecast: true` to alert on forecasted rather than actual spend, and `emails`. `healthCheck` probes a `target` (an address, a hostname, or a reference) from outside the account over `HTTP`, `HTTPS`, or `TCP` on `port`, with `path` for the HTTP kinds, an optional `host` header when the target is an address, `interval` of `10` or `30`, and `failureThreshold` of 1 to 10, and emails the listed addresses on failure: it is a Route 53 health check, an SNS topic named `attune-<name>` with one email subscription per address, and a CloudWatch alarm on the check's status, always in `us-east-1`. Each address receives one confirmation email on the first apply and must accept it. Budgets plan before everything else and health checks after the host kinds, with DNS last; both prune only under `prune.monitoring: true` or `-M/--prune-monitoring`, off by default, and both carry the `attune=managed` tag. A health check's `protocol` or `interval` change plans as `! replace`. A live run groups the specs by provider in name order: it grounds and plans every provider first, printing one grounding line and one `attune plan: provider=<name>` block each, and only then applies, one `attune apply: provider=<name>` block each, so a provider that cannot be reached stops the run before any other provider is changed. With one provider in the store the output is unchanged.

`validate` parses and checks specifications without credentials or provider access. `plan` authenticates and reads live state but does not mutate it. `apply` performs the reviewed create and update actions plus only those deletes enabled by the applicable prune policy. Every live run prints non-secret provider grounding so the operator can confirm its target. Requests are restricted to an ARM/Graph origin allowlist, and non-2xx provider responses are redacted wholesale before reaching any diagnostic or error message.

The change block a live run prints is prospective: change lines and the `N change(s) would be made.` trailer render in yellow, under an uncolored `attune plan: provider=azure` header, for both `plan` and `apply`. `apply` then prints a green confirmation block headed `attune apply: provider=azure` — one green past-tense line (`created`/`updated`/`deleted`) per change, printed as that change lands, so an interrupted run shows exactly what was applied — followed by a green `N change(s) made.` trailer on full success. Color is suppressed automatically for non-TTY output, `NO_COLOR`, and `TERM=dumb`.

DNS zone creation is part of the plan: a spec referencing a zone that does not exist plans as `+ create dnsZone|<zone>` ahead of that zone's record changes, and `apply` creates it through the same reviewed change loop. Existing zones are never rewritten.

An optional `content_version` string in `attune.yaml` declares the content version of the stored specs; when set, attune appends ` content=<value>` to the validate result and to the live grounding line. The convention decouples spec content from repository tags: bump `content_version` when azure-affecting spec content changes, and leave it (and any git tag) alone for unrelated repository changes — attune never reads git.

Normal plans print resource keys and concise summaries, but omit DNS values, tag values, memberships, owners, role actions, credentials, and provider response bodies. `-d`/`--diagnostic` adds non-secret account and target grounding. `-V`/`--verbose` opts in to field-level detail: each planned update gains indented `field: old -> new` lines (added/removed entries for set-valued fields) showing exactly which values drive it — including live tenant values the default output deliberately omits. Resource-group locations are compared by normalized region name, so `East US` in a spec and ARM's `eastus` are the same region, not drift. Apart from its store, the remembered store path, `render` output, and the private temporary file `edit` opens, attune writes no local state, cache, telemetry, copied specs, or diagnostic artifacts; serviced-repository data is sent only to the configured Azure provider endpoints during an operator-requested live command.

## Usage
Run `attune -h` for the full help, including commands, options, and the store path order.

## Store

The specs live in one encrypted store that attune manages itself, so they and the secrets inside them stay sealed in a synced folder and no repository needs to hold them. The store is the same kind of file macfit uses, a SQLite database sealed with XChaCha20-Poly1305, but attune keeps its own: its own file, its own key under the `attune` keychain service, its own remembered path, and its own commands. macfit never sees it.

### What it holds

Every entry is one spec named by a relative path ending in `.yaml` or `.yml`, `res/dns/zone.yaml` for example, plus the configuration named `attune.yaml`. The store keeps the YAML text as written, comments included, and every version ever captured. Parsing happens when `validate`, `plan`, or `apply` run, and once more inside `add` and `edit`, which refuse to save anything that would not validate together with the rest of the store.

### Where it is

Each command finds the store in this order: `-t PATH` on the command line, then `ATTUNE_STORE`, then the path `init -t` remembered in `$XDG_CONFIG_HOME/attune/store`, then `$XDG_DATA_HOME/attune/attune.store`. `init -N -t PATH` creates a store at PATH and remembers it; the folder must already exist, so a missing synced folder is never faked. On another Mac, `init -t PATH` asks for the recovery passphrase once and saves the key to that Mac's login keychain. Every command but `help` and `version` needs macOS, because the key lives in the login keychain.

### Working with it

```text
attune init -N -t ~/data/etc/attune-azure.store   # first Mac: create the store and its key, remember the path
attune add attune.yaml ./attune.yaml              # store the configuration under its name
attune add ./specs                                # import every spec under a directory, named relative to it
attune add res/dns/new.yaml < new.yaml            # store one new spec from standard input
attune edit res/dns/new.yaml                      # change a stored spec in $EDITOR; saved only if it validates
attune ls                                         # entries by name; -b captured for newest first
attune cat attune.yaml                            # one stored file on stdout
attune rename dns/old.yaml dns/new.yaml           # change a name, keeping the versions
attune render                                     # browse a plaintext copy in a private temp directory
attune st                                         # store, key, entries, last save; drift is plan's job
attune init -t ~/data/etc/attune-azure.store      # another Mac: unlock with the passphrase, remember the path
```

`edit` reopens the editor when the result would not validate; quit without changing anything to abort. Drift against Azure is what `plan` reports, so `st` never contacts Azure.

`st` starts with the time of the check (`checked`), so pasted output shows when it was taken, and ends with `last save here` before the drift note. Every command that saves the store adds a row to a short save log inside it, holding the newest 100 saves. It also records the save in `$XDG_STATE_HOME/attune/last-save-KEYID` on this Mac, with mode 0600 and no spec contents. The record names the command and its own arguments, without `-t`. `last save here` is green `in store` while the store still holds that save, and red `lost: COMMAND at TIME` when another Mac's copy has replaced it. That happens when iCloud Drive keeps one Mac's copy and silently drops the other's, with no conflict copy. It shows `none` before this Mac first saves, and `unknown` when the save log is too short to tell or the store does not open. A lost save makes `st` exit 1. Every other command that opens the store, `plan` and `apply` included, prints `warning: this Mac's last save (COMMAND at TIME) is missing from the store` on stderr and carries on. The warning lasts until this Mac saves again, so re-running the named command clears it. After an `add NAME` that read standard input, pipe the content in again. `key show`, `key restore`, `key rm`, and `key passphrase` maintain the keychain item exactly as macfit's do; see macfit's README for the key and passphrase model.

### Adding and removing a record

A record's whole life, using a throwaway CNAME. Store it straight from standard input; attune validates it together with every other stored spec before saving, so a typo is refused here rather than at `plan` time:

```text
attune add res/dns/example.com/cname_scratch.yaml <<'EOF'
kind: dnsRecordSet
zone: example.com
type: CNAME
name: scratch
ttl: 300
values:
  - www.example.com
EOF
```

`attune plan` now lists it as the only change, `+ create dnsRecordSet example.com|CNAME|scratch missing`, and `attune apply` creates it. To see it land, ask one of the zone's own name servers rather than a resolver; `dig NS example.com +short` names them:

```text
dig @ns1-01.azure-dns.com scratch.example.com CNAME +short
```

To take it back out, remove the entry and let DNS pruning do the rest: `attune rm res/dns/example.com/cname_scratch.yaml`, after which `attune plan` shows `- delete dnsRecordSet example.com|CNAME|scratch absent from specs` and `attune apply` deletes it. `rm` also drops the entry's stored versions; use `edit` when the record should stay and only its content changes.

### Moving specs from a repository into the store

Done once, from the root of the repository that holds them:

1. `attune init -N -t ~/data/etc/attune-azure.store` and choose a recovery passphrase.
2. `attune add attune.yaml ./attune.yaml`, then `attune add ./specs`, naming whatever directory held the specs.
3. `attune validate` prints the same count and `content=` line the repository copy gave, and `attune plan` lists the same changes.
4. On another Mac, `attune init -t ~/data/etc/attune-azure.store` with the passphrase, then `attune validate`.

Removing the specs from the repository afterwards is a separate step; attune never deletes them.

## Getting Started
Install the latest release:

```bash
go install github.com/queone/attune/cmd/attune@latest
```

Or clone this repository and run `./build.sh`, which tests, builds, and installs `attune`.

## History
attune lived in [gkit](https://github.com/queone/gkit) through gkit v0.87.0 (attune v1.6.0), then moved to this repository. Its encrypted store code is gkit's `lockbox` package, which attune imports.
