# attune Plan

## Product Direction

A declarative reconciler that keeps Azure DNS, groups, app registrations, roles, and resource groups matching YAML specs kept in an encrypted store, with the live provider as the only source of truth.

## Ideas To Explore

Ideas captured for future reference. A bullet list — each line starts with `- IE<N>: ` (sequential N) for stable references. Two kinds: (a) **idea IE** — `IE<N>: <one-liner>`, awaiting Director discussion and the AC-ceremony evaluation in `AGENTS.md` `### AC-First Workflow`; (b) **AC-pointer** — `IE<N>: <one-liner> → govna/ac<N>-<slug>.md`, pointing at a drafted AC stub not yet through critique. An idea entry that the Director sends to Draft converts to an AC-pointer at AC-draft time, keeping its `IE<N>` number. Remove entries when the idea is rejected, retired, or (for AC-pointers) the AC has shipped and its file deleted. Not a historical record.

- IE2: Add Cloudflare as a DNS provider so a zone moved to Cloudflare's name servers for Cloudflare Tunnel (mud IE85) stays under attune: reconcile `dnsZone` and `dnsRecordSet` specs through the Cloudflare API with an API token from the keychain, keeping the Tunnel's public hostname as an ordinary CNAME to `<tunnel-id>.cfargotunnel.com` and leaving `cloudflared` itself outside attune.
- IE3: Add Oracle Cloud as a provider so a site hosted on OCI's always-free Arm VM (mud IE85) can leave Azure without leaving attune: reconcile `dnsZone` and `dnsRecordSet` specs through the OCI DNS API with an API signing key, and treat compartments as the resource-group analog with the same tag merge semantics.
- IE6: Add `budget` and an uptime check (`cloudwatchAlarm` or a Route 53 health check, decided at draft) for the AWS account; mud needs the budget alert by month two and the uptime check once the site faces the internet. Draft right after AC5 and AC6 ship.
- IE7: Add `iamUser` and Identity Center assignments so the AWS account's sign-ins are declared too; wait for mud's decision to move from an access key to Identity Center.
- IE8: Let a spec value reference another spec's live output, such as a `dnsRecordSet` value naming a `staticAddress`, so the host-then-record flow becomes one apply instead of two.
