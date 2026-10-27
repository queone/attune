# The azure provider

The `azure` provider compares provider-neutral YAML specs directly with live Azure Resource Manager and Microsoft Graph
state. It supports every kind: DNS record sets, security groups, app registrations, role definitions, role assignments, and
resource groups. Command semantics shared by every provider are in [commands.md](commands.md).

## Signing in and configuring

Live commands need `az login`. The `azure` block of `attune.yaml` holds `subscription` and `resource_group`; the
`ARM_SUBSCRIPTION_ID` and `ARM_RESOURCE_GROUP` environment variables override it, and flags override both.

Requests are restricted to an ARM/Graph origin allowlist, and non-2xx provider responses are redacted wholesale before
reaching any diagnostic or error message.

## Kinds

- `dnsRecordSet`: values are compared without ordering sensitivity. The zone apex is `@`. A spec referencing a zone that
  does not exist plans as `+ create dnsZone|<zone>` ahead of that zone's record changes; existing zones are never rewritten.
  DNS pruning defaults to enabled.
- `securityGroup`, `appRegistration`: identity kinds, pruned only when identity pruning is enabled.
- `roleDefinition`, `roleAssignment`: role-assignment and role-definition IDs are derived deterministically, so repeated
  `apply` runs stay stable. Role assignments accept `group` and its case-insensitive `securityGroup` alias,
  `servicePrincipal`, or `user` as `principalType`. A literal directory object ID may omit `principalType`; a named
  principal must provide it, and `attune validate` enforces this rule offline. Role pruning defaults to disabled.
- `resourceGroup`: tags use merge semantics. Locations are compared by normalized region name, so `East US` in a spec and
  ARM's `eastus` are the same region, not drift. Resource-group pruning defaults to disabled.

See [store.md](store.md) for a record's whole life through the store, from `add` to `rm`, and for how to
watch it land on the zone's own name servers.
