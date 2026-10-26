package main

import (
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
}

// Len returns the total number of specs across every kind.
func (b Bundle) Len() int {
	return len(b.Dns) + len(b.Groups) + len(b.Apps) + len(b.RoleDefinitions) + len(b.RoleAssignments) + len(b.ResourceGroups)
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
