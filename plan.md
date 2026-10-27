# attune Plan

## Product Direction

A declarative reconciler that keeps cloud resources matching YAML specs kept in an encrypted store, with the live provider as the only source of truth. Each spec names its provider. `azure` holds DNS, groups, app registrations, roles, and resource groups; `aws` holds Route 53 DNS, a host of IAM role, bucket, firewall, instance, and static address, a cost budget, and an uptime check. Providers register their kinds through one registry so the core stays provider-neutral, and Cloudflare, Oracle Cloud, DigitalOcean, and others follow the same path.

## Ideas To Explore

Ideas captured for future reference. A bullet list — each line starts with `- IE<N>: ` (sequential N) for stable references. Two kinds: (a) **idea IE** — `IE<N>: <one-liner>`, awaiting Director discussion and the AC-ceremony evaluation in `AGENTS.md` `### AC-First Workflow`; (b) **AC-pointer** — `IE<N>: <one-liner> → govna/ac<N>-<slug>.md`, pointing at a drafted AC stub not yet through critique. An idea entry that the Director sends to Draft converts to an AC-pointer at AC-draft time, keeping its `IE<N>` number. Remove entries when the idea is rejected, retired, or (for AC-pointers) the AC has shipped and its file deleted. Not a historical record.

- IE2: Add Cloudflare as a DNS provider so a zone moved to Cloudflare's name servers for Cloudflare Tunnel (mud IE85) stays under attune: reconcile `dnsZone` and `dnsRecordSet` specs through the Cloudflare API with an API token from the keychain, keeping the Tunnel's public hostname as an ordinary CNAME to `<tunnel-id>.cfargotunnel.com` and leaving `cloudflared` itself outside attune.
- IE7: Add `iamUser` and Identity Center assignments so the AWS account's sign-ins are declared too; wait for mud's decision to move from an access key to Identity Center.
- IE9: Let a reference cross providers, such as an Azure record pointing at an AWS address, by ordering apply across providers; the two-pass flow covers it until then.
