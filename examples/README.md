# Attune synthetic example

This directory is a fictional, provider-safe attune project. It demonstrates
all supported resource kinds without containing a usable tenant, subscription,
principal, credential, or production domain.

Validate the files offline through a throwaway store named by `ATTUNE_STORE`,
so the store you normally use is left alone:

```sh
export ATTUNE_STORE=/tmp/attune-example.store
attune init -N                        # any passphrase; the store is throwaway
attune add attune.yaml attune.yaml
attune add specs
attune validate
attune key rm -f && rm "$ATTUNE_STORE" && unset ATTUNE_STORE
```

A second example, `aws/`, is a store for the `aws` provider: an `attune.yaml`
with the `aws` block, one Route 53 record set, and one host declared as an IAM
role, a bucket, a firewall, an instance, and a static address. Validate it the
same way from inside `aws/`. Its live commands need the AWS CLI configured
(`aws configure`); the image parameter path, the TEST-NET source, and the
`example-*` names are placeholders like the Azure example's.

`validate` reads the stored configuration and specifications only. In contrast,
`attune plan` authenticates to the provider and reads live state, while
`attune apply` can create, update, and delete provider resources according to
the reviewed plan and enabled prune policies.

Before any live command, deliberately replace and review every placeholder
class: subscription ID, resource-group name and location, DNS zone and record
data, security-group name and relationships, app-registration name and owners,
role name and permissions, assignment principal and scope, tags, and every
prune setting. Do not run `plan` or `apply` merely by copying this directory.

The zero UUID, `example.com`, TEST-NET address, and `example-attune-*` names are
documentation placeholders. They do not identify deployable infrastructure.
