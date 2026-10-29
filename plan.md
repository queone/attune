# attune Plan

## Product Direction

A declarative reconciler that keeps cloud resources matching YAML specs kept in an encrypted store, with the live provider as the only source of truth. Each spec names its provider. `azure` holds DNS, groups, app registrations, roles, and resource groups; `aws` holds Route 53 DNS, a host of IAM role, bucket, firewall, instance, and static address, a cost budget, and an uptime check. Providers register their kinds through one registry so the core stays provider-neutral, and Cloudflare, Oracle Cloud, DigitalOcean, and others follow the same path.

## Ideas To Explore

Ideas captured for future reference. A bullet list — each line starts with `- IE<N>: ` (sequential N) for stable references. Two kinds: (a) **idea IE** — `IE<N>: <one-liner>`, awaiting Director discussion and the AC-ceremony evaluation in `AGENTS.md` `### AC-First Workflow`; (b) **AC-pointer** — `IE<N>: <one-liner> → govna/ac<N>-<slug>.md`, pointing at a drafted AC stub not yet through critique. An idea entry that the Director sends to Draft converts to an AC-pointer at AC-draft time, keeping its `IE<N>` number. Remove entries when the idea is rejected, retired, or (for AC-pointers) the AC has shipped and its file deleted. Not a historical record.

- IE2: Add Cloudflare as a DNS provider so a zone moved to Cloudflare's name servers for Cloudflare Tunnel (mud IE85) stays under attune: reconcile `dnsZone` and `dnsRecordSet` specs through the Cloudflare API with an API token from the keychain, keeping the Tunnel's public hostname as an ordinary CNAME to `<tunnel-id>.cfargotunnel.com` and leaving `cloudflared` itself outside attune. Deferred: mud chose AWS with the zone staying on Azure (mud decision 2026-10-26), and revisits the Cloudflare shape only if the address cost starts to matter.
- IE3: Warn before a `healthCheck` apply when the region guardrail denies its CloudWatch and SNS calls in `us-east-1`, naming the `UsEast1Partitional` fix from `docs/aws.md`, instead of failing partway after the Route 53 check exists. Open design question: the project account cannot read the organization's policies, CloudWatch and SNS have no dry-run, and a read call that succeeds does not prove the write calls are allowed (mud request, 2026-10-29).
