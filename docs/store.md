# The store

The specs live in one encrypted store that attune manages itself, so they and the secrets inside them stay sealed in a
synced folder and no repository needs to hold them. The store is the same kind of file macfit uses, a SQLite database sealed
with XChaCha20-Poly1305, but attune keeps its own: its own file, its own key under the `attune` keychain service, its own
remembered path, and its own commands. macfit never sees it.

## What it holds

Every entry is one spec named by a relative path ending in `.yaml` or `.yml`, `res/dns/zone.yaml` for example, plus the
configuration named `attune.yaml`. The store keeps the YAML text as written, comments included, and every version ever
captured. Parsing happens when `validate`, `plan`, or `apply` run, and once more inside `add` and `edit`, which refuse to
save anything that would not validate together with the rest of the store.

## Where it is

Each command finds the store in this order: `-t PATH` on the command line, then `ATTUNE_STORE`, then the path `init -t`
remembered in `$XDG_CONFIG_HOME/attune/store`, then `$XDG_DATA_HOME/attune/attune.store`. `init -N -t PATH` creates a store
at PATH and remembers it; the folder must already exist, so a missing synced folder is never faked. On another Mac,
`init -t PATH` asks for the recovery passphrase once and saves the key to that Mac's login keychain. Every command but `help`
and `version` needs macOS, because the key lives in the login keychain.

## Working with it

The examples keep the store in `~/Sync`, standing for any folder your Macs sync, such as one in iCloud Drive.

```text
attune init -N -t ~/Sync/attune.store             # first Mac: create the store and its key, remember the path
attune add attune.yaml ./attune.yaml              # store the configuration under its name
attune add ./specs                                # import every spec under a directory, named relative to it
attune add res/dns/new.yaml < new.yaml            # store one new spec from standard input
attune edit res/dns/new.yaml                      # change a stored spec in $EDITOR; saved only if it validates
attune ls                                         # entries by name; -b captured for newest first
attune cat attune.yaml                            # one stored file on stdout
attune rename dns/old.yaml dns/new.yaml           # change a name, keeping the versions
attune render                                     # browse a plaintext copy in a private temp directory
attune st                                         # store, key, entries, last save; drift is plan's job
attune init -t ~/Sync/attune.store                # another Mac: unlock with the passphrase, remember the path
```

`edit` reopens the editor when the result would not validate; quit without changing anything to abort. Drift against the
provider is what `plan` reports, so `st` never contacts a provider.

`st` starts with the time of the check (`checked`), so pasted output shows when it was taken, and ends with
`last save here`, `written by`, and `update` before the drift note. Every command that saves the store adds a row to a short save log inside it, holding
the newest 100 saves. It also records the save in `$XDG_STATE_HOME/attune/last-save-KEYID` on this Mac, with mode 0600 and
no spec contents. The record names the command and its own arguments, without `-t`. `last save here` is green `in store`
while the store still holds that save, and red `lost: COMMAND at TIME` when another Mac's copy has replaced it. That happens
when iCloud Drive keeps one Mac's copy and silently drops the other's, with no conflict copy. It shows `none` before this
Mac first saves, and `unknown` when the save log is too short to tell or the store does not open. A lost save makes `st`
exit 1. Every other command that opens the store, `plan` and `apply` included, prints
`warning: this Mac's last save (COMMAND at TIME) is missing from the store` on stderr and carries on. The warning lasts
until this Mac saves again, so re-running the named command clears it. After an `add NAME` that read standard input, pipe
the content in again. `key show`, `key restore`, `key rm`, and `key passphrase` maintain the keychain item exactly as
macfit's do; see macfit's README for the key and passphrase model.

Every save also records the newest attune version that ever wrote the store; an older attune saving it never lowers that
version. `written by` shows it, in yellow with the exact `go install github.com/queone/attune/cmd/attune@vX.Y.Z` command
when it is newer than the running attune, which happens when one Mac upgrades before another. Every other command that
opens such a store prints `warning: attune vX.Y.Z last wrote this store, newer than this vA.B.C; run go install ...` on
stderr and carries on. A store last saved by attune v1.14.0 or earlier shows `none recorded`, and those releases neither
record nor read the version, so a Mac still on one is upgraded by hand once.

`update` says whether a newer attune exists: `vX.Y.Z available; run go install github.com/queone/attune/cmd/attune@vX.Y.Z`
in yellow, `none (vX.Y.Z is the latest, checked TIME)`, `skipped (REASON)`, or `unknown (ERROR)`. `st` asks the Go module
proxy, the first `GOPROXY` entry, at most once a day, with a two-second timeout and an hour's wait after a failure, and
keeps the answer in `~/Library/Caches/attune/update-check`. Set `NO_UPDATE_CHECK` to any value to skip it; a development
build and a `GOPROXY` of `off` or `direct` skip it too. Bare `attune` and `attune -h` share the same daily check and,
when a newer release exists and stderr is a terminal, print one line after the help:
`attune: vX.Y.Z available; run go install github.com/queone/attune/cmd/attune@vX.Y.Z`. They print nothing extra otherwise.
No other command checks, so scripts make no such request, and attune never installs anything.

## Adding and removing a record

A record's whole life, using a throwaway CNAME. Store it straight from standard input; attune validates it together with
every other stored spec before saving, so a typo is refused here rather than at `plan` time:

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

`attune plan` now lists it as the only change, `+ create dnsRecordSet example.com|CNAME|scratch missing`, and
`attune apply` creates it. To see it land, ask one of the zone's own name servers rather than a resolver;
`dig NS example.com +short` names them:

```text
dig @ns1-01.azure-dns.com scratch.example.com CNAME +short
```

To take it back out, remove the entry and let DNS pruning do the rest: `attune rm res/dns/example.com/cname_scratch.yaml`,
after which `attune plan` shows `- delete dnsRecordSet example.com|CNAME|scratch absent from specs` and `attune apply`
deletes it. `rm` also drops the entry's stored versions; use `edit` when the record should stay and only its content changes.

## Moving specs from a repository into the store

Done once, from the root of the repository that holds them:

1. `attune init -N -t ~/Sync/attune.store` and choose a recovery passphrase.
2. `attune add attune.yaml ./attune.yaml`, then `attune add ./specs`, naming whatever directory held the specs.
3. `attune validate` prints the same count and `content=` line the repository copy gave, and `attune plan` lists the same
   changes.
4. On another Mac, `attune init -t ~/Sync/attune.store` with the passphrase, then `attune validate`.

Removing the specs from the repository afterwards is a separate step; attune never deletes them.
