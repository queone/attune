package main

import (
	"fmt"
	"sort"
	"strings"
)

// DnsRecord is a provider-neutral DNS record set.
type DnsRecord struct {
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
	Name    string
	Owners  []string
	Members []string
}

// AppRegistration is an application registration.
type AppRegistration struct {
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
	Principal     string
	PrincipalType string
	Role          string
	Scope         Scope
}

// ResourceGroup is a resource group whose declared tags use merge semantics.
type ResourceGroup struct {
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
}

// Len returns the total number of specs across every kind.
func (b Bundle) Len() int {
	return len(b.Dns) + len(b.Groups) + len(b.Apps) + len(b.RoleDefinitions) + len(b.RoleAssignments) + len(b.ResourceGroups)
}

// IsEmpty reports whether the bundle has no specs of any kind.
func (b Bundle) IsEmpty() bool {
	return b.Len() == 0
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
