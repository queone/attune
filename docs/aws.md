# The aws provider

The `aws` provider reconciles Route 53 DNS, a host, and two monitoring kinds through the local `aws` CLI. Command semantics
shared by every provider are in [commands.md](commands.md); the [aws example](../examples/aws/) is a complete store.

## Signing in and configuring

The local `aws` CLI is the provider's only credential source and transport: every call is an allowlisted
`aws <service> <operation>` invocation with JSON output, and a failing call is reported as its command and exit status with
the process output dropped. Sign in with a signed-in AWS CLI profile: `aws login --profile <name>` under an organization that
signs in through Identity Center, or `aws configure` with an access key otherwise.

The `aws` block of `attune.yaml` holds optional `region` and `profile`, the CLI's own defaults otherwise.

## Regions

`us-east-1` is AWS's management region, where IAM, Route 53, STS, and Budgets live; the configured region is where the host
runs. Each family is found where it lives: roles by their path, buckets and health checks by listing them and reading their
tags, so a bucket in any region is found, and EC2 instances, firewalls, and addresses through the tagging API in the
configured region. `validate` prints one `attune: warning:` line on stderr for each `aws.region` or `bucket` `region` that
names `us-east-1` while still reporting `OK`; under the account shape below the warning is a guardrail, and in an account
that never activated advanced features it is only advice.

## The account shape

The `aws` provider expects the account shape AWS builds for a new team. Signing up on the Free plan creates one account,
which AWS names "Index Zero", but the Free plan's service control policy denies Systems Manager and the EC2 lookups the host
needs and withholds Identity Center, so the host kinds need the paid plan with advanced features activated under a team
name. That turns the account into an organization of three: a management account that pays the bills, owns the organization
and its policies, and holds nothing else; an identity account that administers Identity Center; and the original account as
the project account, where workloads go and where attune's `aws` specs land. AWS names all three, and the management account
may rename them from Organizations.

Activation removes the spend limit set during the upgrade and leaves Budgets as the only spending control, which attune's
`budget` kind can declare in the project account. AWS attaches two guardrail policies to the organization root,
`ManagedAccountSecurityControlPolicy` and `AdvancedModeRegionRestrictionSecurityControlPolicy`; `healthCheck` is the one
kind that needs an edit to the region policy, described under Monitoring below. The region guardrail reserves `us-east-1`
for global services, IAM, Route 53, STS, Budgets, and the tagging API, allows regional services such as EC2, Systems
Manager, and S3 only in `us-east-2`, permits a handful of services such as Bedrock and Support in `us-west-2`, and denies
everything elsewhere, so `attune.yaml` `aws.region` and every `bucket` `region` must name `us-east-2`.

Root sign-in is disabled in such an organization, and the member accounts' root users have no usable credentials; the one
working sign-in is the Builder ID user, who signs in at `https://console.aws.amazon.com/`, picks an account on "Choose AWS
sessions", and holds `AccountFullAccessRole` in each; the Identity Center access portal lists no accounts, since sessions are
granted through the console rather than through permission sets. `aws configure sso` cannot see this model for the same
reason; `aws login --profile <name>` signs the CLI in as that role in the project account with a short-lived session the CLI
refreshes, and `aws.profile` in `attune.yaml` names that profile. An account that never activated advanced features has no
region guardrail and signs in with `aws configure`.

## DNS on Route 53

`dnsRecordSet` on Route 53 follows the same rules as on Azure: the zone apex is `@`, values are compared without ordering
sensitivity, and DNS pruning defaults to enabled. TXT values are written unquoted in specs and quoted on the wire, hosted
zones are created implicitly when a record set names one that does not exist, private hosted zones are ignored, and alias
record sets are neither managed nor pruned.

A record value may reference a host's live output, `${staticAddress:<name>:publicIp}` or `${instance:<name>:id}`; see
[commands.md](commands.md) for how references validate, plan, and apply.

## The host kinds

The provider reconciles a host through five provider-neutral kinds:

- `iamRole`: managed policy ARNs, inline policy documents by name, and an instance profile of the same name; the trust
  policy is fixed to EC2.
- `bucket`: region, a public access block that defaults to fully blocked, and `encryption` of `aes256` or a key ARN.
- `firewall`: ingress rules of `protocol`, `fromPort`, `toPort`, and `sources` that are CIDRs or other firewall names, in
  the default VPC unless `vpc` names one by its Name tag.
- `instance`: `type`, `image` as an AMI id or an SSM parameter path resolved when the instance is created, `role`, `firewall`,
  `imdsv2Required` defaulting on, `rootDisk` size, type, and encryption, and tags with merge semantics.
- `staticAddress`: attached to an `instance` by name. The applied line for a new `staticAddress` shows the public address.

Plan and apply run them in dependency order, `iamRole`, `bucket`, `firewall`, `instance`, `staticAddress`, then DNS, and
prune them in reverse. Host pruning is off by default; `prune.hosts: true` or `-H/--prune-hosts` turns it on.

A launch that fails right after its instance profile was created is retried for about thirty seconds, since IAM takes a few
seconds to make a new profile visible, and an address waits up to a minute for its instance to leave `pending`, since it
cannot attach before then.

A change to an immutable field, an instance's `type`, `image`, or root disk type and encryption, or a bucket's `region`,
plans as a `! replace` line and `apply` refuses it, naming the field: remove the spec, apply, then add it back. An `image`
given as a parameter path, such as Canonical's `current` Ubuntu image, is never compared with a running instance, since the
parameter moves with every release; only a literal AMI id that differs plans a replace. To move a running instance to a
newer image, replace it as above.

## Parameter Store as a secret target

An Azure `appRegistration` secret can name a Parameter Store parameter as its `deliverTo`, so `apply` writes a new or
rotated client secret there instead of printing it (see [azure.md](azure.md)). The parameter is a `SecureString` in
`aws.region`, or the CLI's region when that is unset, encrypted with the account's default key. attune reads only whether
it exists, through `ssm describe-parameters`, and writes it through `ssm put-parameter` with the value inside the CLI's
input JSON on standard input, never on a command line. attune never tags, lists, or deletes a parameter. A store whose
only `aws` use is a delivery target still grounds `aws`, before any provider plans, and prints its grounding line with
`specs=0`.

## What attune can prune

Every resource attune creates carries the tag `attune=managed`, IAM roles and instance profiles also live under the path
`/attune/`, and the provider lists only those, so pruning can never reach a resource attune did not create.

## Monitoring

Two monitoring kinds complete the host.

- `budget` is a monthly cost budget: `amount` as a decimal, `currency` defaulting to `USD`, and `alerts` of a `threshold`
  percent, an optional `forecast: true` to alert on forecasted rather than actual spend, and `emails`.
- `healthCheck` probes a `target` (an address, a hostname, or a reference) from outside the account over `HTTP`, `HTTPS`, or
  `TCP` on `port`, with `path` for the HTTP kinds, an optional `host` header when the target is an address, `interval` of
  `10` or `30`, and `failureThreshold` of 1 to 10, and emails the listed addresses on failure. It is a Route 53 health
  check, an SNS topic named `attune-<name>` with one email subscription per address, and a CloudWatch alarm on the check's
  status, all three always in `us-east-1`: Route 53 publishes the check's status metric only there, and an alarm's topic
  must share the alarm's region. Each address receives one confirmation email on the first apply and must accept it. A
  health check's `protocol` or `interval` change plans as `! replace`.

Budgets plan before everything else and health checks after the host kinds, with DNS last; both prune only under
`prune.monitoring: true` or `-M/--prune-monitoring`, off by default, and both carry the `attune=managed` tag.

### Allowing the health check through the region guardrail

The advanced-mode region guardrail denies the health check's SNS and CloudWatch calls in `us-east-1`, so before the first
`healthCheck` apply in such an organization add `cloudwatch:PutMetricAlarm`, `cloudwatch:DeleteAlarms`,
`cloudwatch:TagResource`, and `sns:*` to the `NotAction` list of the policy's `us-east-1` statement, `UsEast1Partitional`,
from the management account's Organizations console. AWS wrote that policy and may rewrite it on a plan or mode change, so
repeat the edit if a health check apply is denied again.
