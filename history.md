# attune History

> One section per release, newest first: what the release added, changed, removed, or deferred in the product, its design, and its code, and why. Governance, toolchain, and release mechanics stay in `CHANGELOG.md` and git. Settled design is in `arch.md`. Search it to learn why a release changed the product.

## v1.18.0

Added:
- Apply prints the provider id for each created instance, Elastic IP, IAM role, firewall, health check, app registration, and security group.
- A created Elastic IP's applied line shows its allocation id, then its public address.
- `attune edit NAME` reads the new spec from standard input when standard input is not a terminal.

Changed:
- Five capability `Put` calls return the provider id; `PutAddress` returns the allocation id and the public address.
- `edit` without a terminal no longer fails with `edit needs a terminal`.

Decisions:
- Update and delete lines keep their summary, since the operator already knows an existing resource's id.
- Buckets, budgets, DNS records and zones, resource groups, and role definitions and assignments show no id.
- An update's `Put` call may return an empty id and makes no extra lookup call.
- Piped `edit` refuses empty input, so a failed pipe step cannot replace a stored spec with nothing.
- Piped `edit` refuses invalid input with nothing saved, since no editor is open to fix it.

Risks:
- No live apply has confirmed a printed id yet; a post-release check on a new instance remains.

## v1.17.0

Changed:
- A health check's apply creates its SNS topic and subscriptions before the Route 53 check, so an SNS denial leaves nothing.
- A rejected SNS or CloudWatch call in `us-east-1` names the region guardrail fix in its error.

Decisions:
- attune does not probe the guardrail before writes; it discards CLI error text, and SNS and CloudWatch have no dry-run.
- A CloudWatch denial leaves the topic and tagged check; the next apply after the guardrail edit adds the alarm.
- A topic left by a failed Route 53 create stays out of scope; prune cannot find it if the spec goes first.

Risks:
- No organization with an unedited guardrail was available to test the denial live.
- The hint omits `cloudwatch:DescribeAlarms`, which plan reads and the documented edit does not list.

## v1.16.1

Changed:
- An instance's live security group shows as its firewall name in a plan filtered with `-k instance`.
- Before, such a plan compared the group id with the spec's name and reported false firewall drift.

Risks:
- The fault existed since v1.9.0; an unfiltered plan listed firewalls first and hid it.

## v1.16.0

Added:
- The `instance` kind accepts optional `metadataHopLimit` from 1 to 64, so containers on a bridge network cannot reach role credentials.
- Launch sets the declared hop limit in `run-instances` metadata options.
- Plan reports a different live hop limit as an instance update; `-V` shows `metadataHopLimit: OLD -> NEW`.
- Apply sets the hop limit with one `modify-instance-metadata-options` call, without a restart.

Changed:
- The `update:` line names a release as the latest only when it equals the running version.
- The `update:` line shows `none (checked TIME)` when the proxy lists an older release, such as before it sees a new tag.
- The `update:` line joins a multi-line error with `; ` on one line.
- gkit moves to v0.96.0, so the first update check after an upgrade asks the module proxy again.

Deferred:
- A pre-apply check of the region guardrail for `healthCheck` calls; no clean probe exists from the project account (plan.md IE3).

Decisions:
- An unset `metadataHopLimit` stays unmanaged, so the image's default remains and existing specs plan no change.
- The guardrail check stays out of this release so the hop-limit fix is not delayed.

Risks:
- No live AWS call has confirmed the combined `--http-tokens` and `--http-put-response-hop-limit` call; a post-release check on mud's VM covers it.
