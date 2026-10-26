package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// DnsRecord is a provider-neutral DNS record set. Provider names the
// registered provider a desired record belongs to; live records leave it
// empty.
type DnsRecord struct {
	Provider   string
	Zone       string
	RecordType string
	Name       string
	TTL        int64
	Values     []string
}

// Key identifies a DNS record set independent of its live/desired origin.
func (r DnsRecord) Key() string {
	return fmt.Sprintf("%s|%s|%s", r.Zone, strings.ToUpper(r.RecordType), r.Name)
}

// SameData reports whether two DNS records carry the same TTL and value set,
// comparing values order-insensitively after trimming whitespace.
func (r DnsRecord) SameData(other DnsRecord) bool {
	normalize := func(values []string) []string {
		out := make([]string, len(values))
		for i, v := range values {
			out[i] = strings.TrimSpace(v)
		}
		sort.Strings(out)
		return out
	}
	a, b := normalize(r.Values), normalize(other.Values)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return r.TTL == other.TTL
}

// SecurityGroup is a security group with owner and member directory objects.
type SecurityGroup struct {
	Provider string
	Name     string
	Owners   []string
	Members  []string
}

// AppRegistration is an application registration.
type AppRegistration struct {
	Provider         string
	Name             string
	Owners           []string
	ServicePrincipal bool
}

// Scope is an Azure RBAC scope.
type Scope struct {
	ManagementGroup string
	Subscription    string
	ResourceGroup   string
	DnsZone         string
}

// ArmID resolves the scope to a fully qualified ARM resource ID, falling
// back to defaultSubscription when the scope doesn't declare its own.
func (s Scope) ArmID(defaultSubscription string) (string, error) {
	if s.ManagementGroup != "" {
		return fmt.Sprintf("/providers/Microsoft.Management/managementGroups/%s", s.ManagementGroup), nil
	}
	subscription := s.Subscription
	if subscription == "" {
		subscription = defaultSubscription
	}
	if subscription == "" {
		return "", fmt.Errorf("scope needs a subscription; set it in the spec or target config")
	}
	if s.DnsZone != "" && s.ResourceGroup == "" {
		return "", fmt.Errorf("dnsZone scope requires a resourceGroup")
	}
	id := fmt.Sprintf("/subscriptions/%s", subscription)
	if s.ResourceGroup != "" {
		id += fmt.Sprintf("/resourceGroups/%s", s.ResourceGroup)
	}
	if s.DnsZone != "" {
		id += fmt.Sprintf("/providers/Microsoft.Network/dnsZones/%s", s.DnsZone)
	}
	return id, nil
}

// RoleDefinition is a custom RBAC role definition.
type RoleDefinition struct {
	Provider         string
	Name             string
	Description      string
	AssignableScopes []Scope
	Actions          []string
	NotActions       []string
	DataActions      []string
	NotDataActions   []string
}

// RoleAssignment is a role assignment.
type RoleAssignment struct {
	Provider      string
	Principal     string
	PrincipalType string
	Role          string
	Scope         Scope
}

// ResourceGroup is a resource group whose declared tags use merge semantics.
type ResourceGroup struct {
	Provider string
	Name     string
	Location string
	Tags     map[string]string
}

// Bundle holds every supported desired resource kind.
type Bundle struct {
	Dns             []DnsRecord
	Groups          []SecurityGroup
	Apps            []AppRegistration
	RoleDefinitions []RoleDefinition
	RoleAssignments []RoleAssignment
	ResourceGroups  []ResourceGroup
	IamRoles        []IamRole
	Buckets         []Bucket
	Firewalls       []Firewall
	Instances       []Instance
	Addresses       []StaticAddress
}

// Len returns the total number of specs across every kind.
func (b Bundle) Len() int {
	return len(b.Dns) + len(b.Groups) + len(b.Apps) + len(b.RoleDefinitions) + len(b.RoleAssignments) + len(b.ResourceGroups) +
		len(b.IamRoles) + len(b.Buckets) + len(b.Firewalls) + len(b.Instances) + len(b.Addresses)
}

// IsEmpty reports whether the bundle has no specs of any kind.
func (b Bundle) IsEmpty() bool {
	return b.Len() == 0
}

// Providers returns the distinct provider names the bundle's specs use,
// sorted, so a live run visits providers in a stable order.
func (b Bundle) Providers() []string {
	seen := map[string]bool{}
	for _, item := range b.Dns {
		seen[item.Provider] = true
	}
	for _, item := range b.Groups {
		seen[item.Provider] = true
	}
	for _, item := range b.Apps {
		seen[item.Provider] = true
	}
	for _, item := range b.RoleDefinitions {
		seen[item.Provider] = true
	}
	for _, item := range b.RoleAssignments {
		seen[item.Provider] = true
	}
	for _, item := range b.ResourceGroups {
		seen[item.Provider] = true
	}
	for _, item := range b.IamRoles {
		seen[item.Provider] = true
	}
	for _, item := range b.Buckets {
		seen[item.Provider] = true
	}
	for _, item := range b.Firewalls {
		seen[item.Provider] = true
	}
	for _, item := range b.Instances {
		seen[item.Provider] = true
	}
	for _, item := range b.Addresses {
		seen[item.Provider] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ForProvider returns the subset of the bundle whose specs name provider,
// preserving each kind's order.
func (b Bundle) ForProvider(provider string) *Bundle {
	part := &Bundle{}
	for _, item := range b.Dns {
		if item.Provider == provider {
			part.Dns = append(part.Dns, item)
		}
	}
	for _, item := range b.Groups {
		if item.Provider == provider {
			part.Groups = append(part.Groups, item)
		}
	}
	for _, item := range b.Apps {
		if item.Provider == provider {
			part.Apps = append(part.Apps, item)
		}
	}
	for _, item := range b.RoleDefinitions {
		if item.Provider == provider {
			part.RoleDefinitions = append(part.RoleDefinitions, item)
		}
	}
	for _, item := range b.RoleAssignments {
		if item.Provider == provider {
			part.RoleAssignments = append(part.RoleAssignments, item)
		}
	}
	for _, item := range b.ResourceGroups {
		if item.Provider == provider {
			part.ResourceGroups = append(part.ResourceGroups, item)
		}
	}
	for _, item := range b.IamRoles {
		if item.Provider == provider {
			part.IamRoles = append(part.IamRoles, item)
		}
	}
	for _, item := range b.Buckets {
		if item.Provider == provider {
			part.Buckets = append(part.Buckets, item)
		}
	}
	for _, item := range b.Firewalls {
		if item.Provider == provider {
			part.Firewalls = append(part.Firewalls, item)
		}
	}
	for _, item := range b.Instances {
		if item.Provider == provider {
			part.Instances = append(part.Instances, item)
		}
	}
	for _, item := range b.Addresses {
		if item.Provider == provider {
			part.Addresses = append(part.Addresses, item)
		}
	}
	return part
}

// normalized returns a deduplicated, sorted copy of values.
func normalized(values []string) []string {
	out := append([]string{}, values...)
	sort.Strings(out)
	out = dedupSorted(out)
	return out
}

func dedupSorted(values []string) []string {
	if len(values) == 0 {
		return values
	}
	out := values[:1]
	for _, v := range values[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// tagsSatisfy reports whether every declared tag is present with the same
// value in live. Live-only tags are ignored (merge semantics).
func tagsSatisfy(declared, live map[string]string) bool {
	for key, value := range declared {
		if live[key] != value {
			return false
		}
	}
	return true
}

// FieldDiff records one field-level difference behind a planned update,
// printed only under -V/--verbose. A scalar change fills Old and New; a
// set-valued change fills only the side that applies (entries added or
// entries removed).
type FieldDiff struct {
	Field string
	Old   string
	New   string
}

// sameLocation reports whether two Azure location names refer to the same
// region, tolerating display formatting ("East US") against ARM's
// normalized form ("eastus").
func sameLocation(a, b string) bool {
	return normalizeLocation(a) == normalizeLocation(b)
}

func normalizeLocation(location string) string {
	return strings.ToLower(strings.ReplaceAll(location, " ", ""))
}

// IamRole is a role an instance assumes, with managed policies by ARN,
// inline policy documents by name, and an optional instance profile of the
// same name. The trust policy is fixed to the provider's compute service.
type IamRole struct {
	Provider        string
	Name            string
	ManagedPolicies []string
	InlinePolicies  map[string]any
	InstanceProfile bool
}

// PublicAccessBlock is a bucket's four public-access settings; a spec that
// omits the block gets every setting on.
type PublicAccessBlock struct {
	BlockPublicAcls       bool
	IgnorePublicAcls      bool
	BlockPublicPolicy     bool
	RestrictPublicBuckets bool
}

// Bucket is an object-storage bucket with its region, public access block,
// and default encryption, `aes256` or a key ARN.
type Bucket struct {
	Provider          string
	Name              string
	Region            string
	PublicAccessBlock PublicAccessBlock
	Encryption        string
}

// FirewallRule is one ingress rule: a protocol, a port range, and sources
// that are CIDRs or the names of other firewalls.
type FirewallRule struct {
	Protocol string
	FromPort int64
	ToPort   int64
	Sources  []string
}

// Firewall is a named ingress rule set in a network, the default one unless
// VPC names another by its Name tag. ID is the live identifier, empty in a
// spec.
type Firewall struct {
	Provider string
	ID       string
	Name     string
	VPC      string
	Rules    []FirewallRule
}

// RootDisk is an instance's root volume.
type RootDisk struct {
	Size      int64
	Type      string
	Encrypted bool
}

// Instance is a virtual machine named by tag. Image is an image id or a
// parameter path the provider resolves. Tags use merge semantics. ID is the
// live identifier, empty in a spec.
type Instance struct {
	Provider       string
	ID             string
	Name           string
	Type           string
	Image          string
	Role           string
	Firewall       string
	Imdsv2Required bool
	RootDisk       RootDisk
	Tags           map[string]string
}

// StaticAddress is a public address named by tag and attached to an
// instance. ID is the live allocation, PublicIP the live address; both are
// empty in a spec.
type StaticAddress struct {
	Provider string
	ID       string
	Name     string
	Instance string
	PublicIP string
}

// key renders a rule in a canonical form for set comparison.
func (r FirewallRule) key() string {
	return fmt.Sprintf("%s|%d|%d|%s", strings.ToLower(r.Protocol), r.FromPort, r.ToPort, strings.Join(normalized(r.Sources), ","))
}

// sameRules reports whether two rule sets are equal regardless of order.
func sameRules(a, b []FirewallRule) bool {
	return stringsEqual(ruleKeys(a), ruleKeys(b))
}

// ruleKeys renders a rule set as sorted one-source keys, so two sets that
// group the same sources differently compare equal.
func ruleKeys(rules []FirewallRule) []string {
	atomic := atomicRules(rules)
	keys := make([]string, 0, len(atomic))
	for _, r := range atomic {
		keys = append(keys, r.key())
	}
	sort.Strings(keys)
	return keys
}

// atomicRules expands every rule to one rule per source.
func atomicRules(rules []FirewallRule) []FirewallRule {
	var out []FirewallRule
	for _, r := range rules {
		for _, source := range r.Sources {
			out = append(out, FirewallRule{Protocol: r.Protocol, FromPort: r.FromPort, ToPort: r.ToPort, Sources: []string{source}})
		}
	}
	return out
}

// canonicalJSON renders a policy document with sorted keys so a spec's
// mapping compares equal to the live document.
func canonicalJSON(document any) string {
	b, err := json.Marshal(document)
	if err != nil {
		return fmt.Sprintf("%v", document)
	}
	return string(b)
}

// samePolicies reports whether two inline policy sets hold the same
// documents under the same names.
func samePolicies(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for name, doc := range a {
		other, ok := b[name]
		if !ok || canonicalJSON(doc) != canonicalJSON(other) {
			return false
		}
	}
	return true
}
