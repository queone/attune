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
- `securityGroup`: owners and members; pruned only when identity pruning is enabled.
- `appRegistration`: name, owners, and `servicePrincipal`, plus the optional sign-in fields below; pruned only when
  identity pruning is enabled.
- `roleDefinition`, `roleAssignment`: role-assignment and role-definition IDs are derived deterministically, so repeated
  `apply` runs stay stable. Role assignments accept `group` and its case-insensitive `securityGroup` alias,
  `servicePrincipal`, or `user` as `principalType`. A literal directory object ID may omit `principalType`; a named
  principal must provide it, and `attune validate` enforces this rule offline. Role pruning defaults to disabled.
- `resourceGroup`: tags use merge semantics. Locations are compared by normalized region name, so `East US` in a spec and
  ARM's `eastus` are the same region, not drift. Resource-group pruning defaults to disabled.

## App registrations for sign-in

An `appRegistration` can declare what a web sign-in over OIDC needs. Every field below is optional, and an unset field is
unmanaged: it is neither sent nor compared, so a registration configured in the portal keeps its settings until a spec
names them.

```yaml
kind: appRegistration
name: example-site
servicePrincipal: true
signInAudience: AzureADMyOrg
redirectUris:
  - https://example.com/auth/callback
idTokenIssuance: true
secrets:
  - name: host
    validForDays: 365
    issuedAfter: 2026-10-01
tenant: 11111111-1111-1111-1111-111111111111
```

- `signInAudience` takes one of Graph's values: `AzureADMyOrg`, `AzureADMultipleOrgs`,
  `AzureADandPersonalMicrosoftAccount`, or `PersonalMicrosoftAccount`.
- `redirectUris` lists the web platform's redirect URIs, absolute `https://` URLs or `http://localhost` with any port and
  path, compared as a set. An empty list clears the live URIs.
- `idTokenIssuance` is the web platform's ID-token setting.
- `secrets` names the client secrets by display name, each with `validForDays` (1 to 730, default 730) and an optional
  `issuedAfter` date or RFC 3339 time. Each secret is its own change line, keyed
  `appRegistration|<name> secret <secret-name>`. A named secret missing live plans a create. A secret whose live
  credential has expired, or was issued before `issuedAfter`, plans `rotate: expired` or `rotate: issued before <time>`:
  `apply` adds a new credential and removes the old one. To rotate a secret that may have leaked, set `issuedAfter` to now
  and apply. When `secrets` is set, a live secret it does not name is deleted under identity pruning; when it is omitted,
  live secrets are untouched. `validForDays` applies when a secret is created or rotated and is never a drift source.
- `apply` prints a new or rotated secret's value once, on its green applied line as `value <secret> (shown once)`. `plan`
  never shows a value, and `-V` shows only the live credential's dates. attune stores nothing; copy the value where the
  application reads it.
- `tenant` places the registration in another directory than the signed-in one, by tenant id or verified domain, for a
  consumer sign-in tenant such as Entra External ID. The same `name` may then appear once per tenant, and the change key
  becomes `appRegistration|<name>@<tenant>`. attune takes a Graph token for that tenant through the signed-in `az` CLI at
  the first call that needs it, during plan, so a tenant it cannot reach stops the run before any change with
  `run \`az login --tenant <tenant>\` and retry`. Identity pruning lists each named tenant as well as the signed-in one,
  and the `-d` diagnostic line adds ` tenants=<home>,<other>` when a spec names another tenant. Groups, roles, and DNS
  stay in the signed-in tenant.

See [store.md](store.md) for a record's whole life through the store, from `add` to `rm`, and for how to
watch it land on the zone's own name servers.
