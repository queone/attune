# attune Plan

## Product Direction

A declarative reconciler that keeps Azure DNS, groups, app registrations, roles, and resource groups matching YAML specs kept in an encrypted store, with the live provider as the only source of truth.

## Ideas To Explore

Ideas captured for future reference. A bullet list — each line starts with `- IE<N>: ` (sequential N) for stable references. Two kinds: (a) **idea IE** — `IE<N>: <one-liner>`, awaiting Director discussion and the AC-ceremony evaluation in `AGENTS.md` `### AC-First Workflow`; (b) **AC-pointer** — `IE<N>: <one-liner> → govna/ac<N>-<slug>.md`, pointing at a drafted AC stub not yet through critique. An idea entry that the Director sends to Draft converts to an AC-pointer at AC-draft time, keeping its `IE<N>` number. Remove entries when the idea is rejected, retired, or (for AC-pointers) the AC has shipped and its file deleted. Not a historical record.

- IE2: Add Cloudflare as a DNS provider so a zone moved to Cloudflare's name servers for Cloudflare Tunnel (mud IE85) stays under attune: reconcile `dnsZone` and `dnsRecordSet` specs through the Cloudflare API with an API token from the keychain, keeping the Tunnel's public hostname as an ordinary CNAME to `<tunnel-id>.cfargotunnel.com` and leaving `cloudflared` itself outside attune.
- IE7: Add `iamUser` and Identity Center assignments so the AWS account's sign-ins are declared too; wait for mud's decision to move from an access key to Identity Center.
- IE9: Let a reference cross providers, such as an Azure record pointing at an AWS address, by ordering apply across providers; the two-pass flow covers it until then.
- IE10: Give `healthCheck` a region of its own instead of `us-east-1`: AWS's advanced-mode accounts carry a region guardrail that reserves `us-east-1` for global services, IAM, Route 53, Budgets, STS, as AWS now recommends, and denies regional writes there, including `cloudwatch:PutMetricAlarm` and SNS, so the alarm and topic of a health check must land in a workload region such as `us-east-2` while the Route 53 check itself stays global; the metric the alarm reads is published only in `us-east-1`, so the design may need a CloudWatch Synthetics or a second-hand metric instead of the Route 53 one.
