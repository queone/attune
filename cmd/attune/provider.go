package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// DNSProvider is the capability a provider offers for dnsRecordSet specs.
type DNSProvider interface {
	EnsureZone(zone string) error
	HasZone(zone string) (bool, error)
	ListDNS(zone string) ([]DnsRecord, error)
	PutDNS(record DnsRecord) error
	DeleteDNS(record DnsRecord) error
}

// IdentityProvider is the capability a provider offers for securityGroup
// and appRegistration specs.
type IdentityProvider interface {
	GetGroup(name string) (*SecurityGroup, error)
	ListGroupNames() ([]string, error)
	PutGroup(group SecurityGroup) error
	DeleteGroup(name string) error
	GetApp(tenant, name string) (*AppRegistration, error)
	ListAppNames(tenant string) ([]string, error)
	PutApp(app AppRegistration) error
	DeleteApp(tenant, name string) error
	AddAppSecret(app AppRegistration, secret Secret) (string, error)
	RemoveAppSecret(app AppRegistration, keyID string) error
}

// RoleProvider is the capability a provider offers for roleDefinition and
// roleAssignment specs.
type RoleProvider interface {
	ListRoleDefinitions(subscription string) ([]RoleDefinition, error)
	PutRoleDefinition(role RoleDefinition, subscription string) error
	DeleteRoleDefinition(name, subscription string) error
	ResolvePrincipal(name, principalType string) (string, error)
	ResolveRole(name, subscription string) (string, error)
	ListRoleAssignments(scope string) ([]LiveAssignment, error)
	PutRoleAssignment(principal, role, scope string) error
	DeleteRoleAssignment(assignment LiveAssignment) error
}

// ResourceGroupProvider is the capability a provider offers for
// resourceGroup specs.
type ResourceGroupProvider interface {
	ListResourceGroups() ([]ResourceGroup, error)
	PutResourceGroup(group ResourceGroup) error
	DeleteResourceGroup(name string) error
}

// IamRoleProvider is the capability a provider offers for iamRole specs.
// Live roles are the ones the provider itself created.
type IamRoleProvider interface {
	ListIamRoles() ([]IamRole, error)
	PutIamRole(role IamRole) error
	DeleteIamRole(role IamRole) error
}

// StorageProvider is the capability a provider offers for bucket specs.
// Live buckets are the ones the provider itself created.
type StorageProvider interface {
	ListBuckets() ([]Bucket, error)
	PutBucket(bucket Bucket) error
	DeleteBucket(bucket Bucket) error
}

// NetworkProvider is the capability a provider offers for firewall specs.
// Live firewalls are the ones the provider itself created.
type NetworkProvider interface {
	ListFirewalls() ([]Firewall, error)
	PutFirewall(firewall Firewall) error
	DeleteFirewall(firewall Firewall) error
}

// ComputeProvider is the capability a provider offers for instance and
// staticAddress specs. Live instances and addresses are the ones the
// provider itself created. ResolveImage turns a parameter path into an
// image id and returns an image id unchanged. PutInstance returns the id of
// an instance it launched, empty on an update; PutAddress returns the
// public address. Both feed references and the applied line.
type ComputeProvider interface {
	ResolveImage(image string) (string, error)
	ListInstances() ([]Instance, error)
	PutInstance(instance Instance) (string, error)
	DeleteInstance(instance Instance) error
	ListAddresses() ([]StaticAddress, error)
	PutAddress(address StaticAddress) (string, error)
	DeleteAddress(address StaticAddress) error
}

// MonitoringProvider is the capability a provider offers for budget and
// healthCheck specs. Live budgets and checks are the ones the provider
// itself created.
type MonitoringProvider interface {
	ListBudgets() ([]Budget, error)
	PutBudget(budget Budget) error
	DeleteBudget(budget Budget) error
	ListHealthChecks() ([]HealthCheck, error)
	PutHealthCheck(check HealthCheck) error
	DeleteHealthCheck(check HealthCheck) error
}

// Provider is the union of the Azure-shaped capabilities: DNS, identity,
// roles, and resource groups. A provider that supports every one of those
// kinds implements it; a narrower provider implements only its capabilities.
type Provider interface {
	DNSProvider
	IdentityProvider
	RoleProvider
	ResourceGroupProvider
}

// LiveProvider is what the registry constructs for a live run: it can
// authenticate and describe its non-secret target, and it offers one or
// more capability interfaces that Plan and Apply assert per kind family.
type LiveProvider interface {
	// Ground authenticates and returns the account the run operates as.
	Ground() (Account, error)
	// Describe renders the non-secret diagnostic fields of the target.
	Describe() string
}

// ProviderSpec registers one provider: its name as used by the spec
// `provider` field, the kinds it supports, the attune.yaml block it reads,
// and the constructor for a live run.
type ProviderSpec struct {
	Name        string
	Kinds       []string
	ConfigBlock string
	New         func(settings Settings, block map[string]any) (LiveProvider, error)
}

var registry = map[string]ProviderSpec{}

// Register adds or replaces a provider in the registry.
func Register(spec ProviderSpec) {
	registry[spec.Name] = spec
}

// LookupProvider returns the registered provider for name, or an error that
// names it and lists every registered provider.
func LookupProvider(name string) (ProviderSpec, error) {
	spec, ok := registry[name]
	if !ok {
		return ProviderSpec{}, fmt.Errorf("unknown provider %q; registered: %s", name, strings.Join(RegisteredProviders(), ", "))
	}
	return spec, nil
}

// RegisteredProviders returns every registered provider name, sorted.
func RegisteredProviders() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// registeredConfigBlocks returns the attune.yaml block name of every
// registered provider that declares one, sorted.
func registeredConfigBlocks() []string {
	var blocks []string
	for _, spec := range registry {
		if spec.ConfigBlock != "" {
			blocks = append(blocks, spec.ConfigBlock)
		}
	}
	sort.Strings(blocks)
	return blocks
}

// checkKindSupport fails when provider is unregistered or does not support
// kind.
func checkKindSupport(provider, kind string) error {
	spec, err := LookupProvider(provider)
	if err != nil {
		return err
	}
	if !slices.Contains(spec.Kinds, kind) {
		return unsupportedKind(provider, kind)
	}
	return nil
}

// snapshotRegistry returns a copy of the registry so a test can register
// providers and restore the original set afterwards.
func snapshotRegistry() map[string]ProviderSpec {
	copied := make(map[string]ProviderSpec, len(registry))
	maps.Copy(copied, registry)
	return copied
}

// restoreRegistry replaces the registry with a snapshot.
func restoreRegistry(snapshot map[string]ProviderSpec) {
	registry = snapshot
}

// unsupportedKind is the error Plan and Apply return when the provider
// handed to them lacks the capability a kind needs.
func unsupportedKind(provider, kind string) error {
	return fmt.Errorf("provider %q does not support kind %q", provider, kind)
}
