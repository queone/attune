package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeProvider is a hand-rolled Provider double for Plan/Apply tests,
// mirroring rkit's AssignmentProvider test fixture. It implements
// LiveProvider and every capability interface.
type fakeProvider struct {
	name            string
	assignments     []LiveAssignment
	dns             []DnsRecord
	zones           []string
	ensured         []string
	listed          []string
	groups          []SecurityGroup
	groupNames      []string
	apps            []AppRegistration
	appNames        []string
	secretsAdded    []string
	secretsRemoved  []string
	roleDefinitions []RoleDefinition
	resourceGroups  []ResourceGroup
}

var (
	_ Provider     = (*fakeProvider)(nil)
	_ LiveProvider = (*fakeProvider)(nil)
)

func (p *fakeProvider) Ground() (Account, error) {
	return Account{Tenant: "tenant-" + p.name, Subscription: "sub-" + p.name, Identity: "id-" + p.name}, nil
}
func (p *fakeProvider) Describe() string { return "fake=" + p.name }

// dnsOnlyProvider implements LiveProvider and DNSProvider only, standing in
// for a provider that cannot hold identity, role, or resource-group specs.
type dnsOnlyProvider struct {
	dns fakeProvider
}

var (
	_ LiveProvider = (*dnsOnlyProvider)(nil)
	_ DNSProvider  = (*dnsOnlyProvider)(nil)
)

func (p *dnsOnlyProvider) Ground() (Account, error)                 { return p.dns.Ground() }
func (p *dnsOnlyProvider) Describe() string                         { return p.dns.Describe() }
func (p *dnsOnlyProvider) EnsureZone(zone string) error             { return p.dns.EnsureZone(zone) }
func (p *dnsOnlyProvider) HasZone(zone string) (bool, error)        { return p.dns.HasZone(zone) }
func (p *dnsOnlyProvider) ListDNS(zone string) ([]DnsRecord, error) { return p.dns.ListDNS(zone) }
func (p *dnsOnlyProvider) PutDNS(r DnsRecord) error                 { return p.dns.PutDNS(r) }
func (p *dnsOnlyProvider) DeleteDNS(r DnsRecord) error              { return p.dns.DeleteDNS(r) }

func (p *fakeProvider) EnsureZone(zone string) error {
	p.ensured = append(p.ensured, zone)
	return nil
}

// HasZone treats a nil zone list as "every zone exists" so pre-existing
// fixtures keep their behavior.
func (p *fakeProvider) HasZone(zone string) (bool, error) {
	if p.zones == nil {
		return true, nil
	}
	return slices.Contains(p.zones, zone), nil
}

func (p *fakeProvider) ListDNS(zone string) ([]DnsRecord, error) {
	p.listed = append(p.listed, zone)
	return p.dns, nil
}
func (p *fakeProvider) PutDNS(DnsRecord) error                { return nil }
func (p *fakeProvider) DeleteDNS(DnsRecord) error             { return nil }
func (p *fakeProvider) ListGroupNames() ([]string, error)     { return p.groupNames, nil }
func (p *fakeProvider) PutGroup(SecurityGroup) error          { return nil }
func (p *fakeProvider) DeleteGroup(string) error              { return nil }
func (p *fakeProvider) ListAppNames(string) ([]string, error) { return p.appNames, nil }
func (p *fakeProvider) PutApp(AppRegistration) error          { return nil }
func (p *fakeProvider) DeleteApp(string, string) error        { return nil }
func (p *fakeProvider) AddAppSecret(app AppRegistration, secret Secret) (string, string, error) {
	p.secretsAdded = append(p.secretsAdded, app.Key()+" "+secret.Name)
	return "k-new-" + secret.Name, "synthetic-secret-value", nil
}
func (p *fakeProvider) RemoveAppSecret(_ AppRegistration, keyID string) error {
	p.secretsRemoved = append(p.secretsRemoved, keyID)
	return nil
}
func (p *fakeProvider) ListRoleDefinitions(string) ([]RoleDefinition, error) {
	return p.roleDefinitions, nil
}
func (p *fakeProvider) PutRoleDefinition(RoleDefinition, string) error  { return nil }
func (p *fakeProvider) DeleteRoleDefinition(string, string) error       { return nil }
func (p *fakeProvider) ResolvePrincipal(name, _ string) (string, error) { return "id-" + name, nil }
func (p *fakeProvider) ResolveRole(name, _ string) (string, error)      { return "role-" + name, nil }
func (p *fakeProvider) ListRoleAssignments(string) ([]LiveAssignment, error) {
	return p.assignments, nil
}
func (p *fakeProvider) PutRoleAssignment(string, string, string) error { return nil }
func (p *fakeProvider) DeleteRoleAssignment(LiveAssignment) error      { return nil }
func (p *fakeProvider) ListResourceGroups() ([]ResourceGroup, error)   { return p.resourceGroups, nil }
func (p *fakeProvider) PutResourceGroup(ResourceGroup) error           { return nil }
func (p *fakeProvider) DeleteResourceGroup(string) error               { return nil }

func (p *fakeProvider) GetGroup(name string) (*SecurityGroup, error) {
	for _, g := range p.groups {
		if g.Name == name {
			found := g
			return &found, nil
		}
	}
	return nil, nil
}

func (p *fakeProvider) GetApp(tenant, name string) (*AppRegistration, error) {
	for _, a := range p.apps {
		if a.Name == name && a.Tenant == tenant {
			found := a
			return &found, nil
		}
	}
	return nil, nil
}

func TestApexProviderRecordsAreProtected(t *testing.T) {
	item := DnsRecord{Zone: "example.com", RecordType: "NS", Name: "@", TTL: 60, Values: []string{"ns.example.com"}}
	if !dnsProtected(item) {
		t.Errorf("expected apex NS record to be protected")
	}
}

// TestDNSPruneNeverDeletesApexSOAOrNS exercises dnsProtected through the
// full Plan path, not just the pure predicate — AT14.
func TestDNSPruneNeverDeletesApexSOAOrNS(t *testing.T) {
	provider := &fakeProvider{
		dns: []DnsRecord{
			{Zone: "example.com", RecordType: "SOA", Name: "@", TTL: 3600, Values: []string{"ns1.example.com admin.example.com 1 3600 600 86400 60"}},
			{Zone: "example.com", RecordType: "NS", Name: "@", TTL: 3600, Values: []string{"ns1.example.com"}},
			{Zone: "example.com", RecordType: "A", Name: "stale", TTL: 300, Values: []string{"192.0.2.5"}},
		},
	}
	bundle := &Bundle{Dns: []DnsRecord{{Zone: "example.com", RecordType: "A", Name: "www", TTL: 300, Values: []string{"192.0.2.10"}}}}
	changes, err := Plan(provider, bundle, &Options{PruneDNS: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var deleted []string
	for _, c := range changes {
		if c.Action == ActionDelete {
			deleted = append(deleted, c.Target.Dns.Name+"|"+c.Target.Dns.RecordType)
		}
	}
	if len(deleted) != 1 || deleted[0] != "stale|A" {
		t.Errorf("deleted = %v, want only [stale|A] (apex SOA/NS must be protected)", deleted)
	}
}

func TestPruneRetainsAllDesiredAssignmentsAtSharedScope(t *testing.T) {
	scope := Scope{Subscription: "00000000-0000-0000-0000-000000000001"}
	scopeID, err := scope.ArmID("")
	if err != nil {
		t.Fatal(err)
	}
	bundle := &Bundle{
		RoleAssignments: []RoleAssignment{
			{Principal: "one", PrincipalType: "group", Role: "reader", Scope: scope},
			{Principal: "two", PrincipalType: "group", Role: "reader", Scope: scope},
		},
	}
	provider := &fakeProvider{
		assignments: []LiveAssignment{
			{ID: "assignment-one", PrincipalID: "id-one", RoleID: "role-reader", Scope: scopeID},
			{ID: "assignment-two", PrincipalID: "id-two", RoleID: "role-reader", Scope: scopeID},
		},
	}
	changes, err := Plan(provider, bundle, &Options{Subscription: scope.Subscription, PruneRoles: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none", changes)
	}
}

// TestPlansEveryKindInLegacyCompatibleOrder pins the fixed per-kind
// processing order — AT7.
func TestPlansEveryKindInLegacyCompatibleOrder(t *testing.T) {
	subscription := "00000000-0000-0000-0000-000000000001"
	bundle := &Bundle{
		ResourceGroups: []ResourceGroup{{Name: "resources", Location: "eastus"}},
		Groups:         []SecurityGroup{{Name: "readers"}},
		Apps:           []AppRegistration{{Name: "application", ServicePrincipal: true}},
		RoleDefinitions: []RoleDefinition{{
			Name:             "reader",
			Description:      "synthetic role",
			AssignableScopes: []Scope{{Subscription: subscription}},
			Actions:          []string{"Microsoft.Network/dnsZones/read"},
		}},
		RoleAssignments: []RoleAssignment{{Principal: "readers", PrincipalType: "group", Role: "reader", Scope: Scope{Subscription: subscription}}},
		Dns:             []DnsRecord{{Zone: "example.com", RecordType: "A", Name: "docs", TTL: 300, Values: []string{"192.0.2.10"}}},
	}
	changes, err := Plan(&fakeProvider{}, bundle, &Options{Subscription: subscription})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []string{"resourceGroup", "securityGroup", "appRegistration", "roleDefinition", "roleAssignment", "dnsRecordSet"}
	if len(changes) != len(want) {
		t.Fatalf("got %d changes, want %d: %+v", len(changes), len(want), changes)
	}
	for i, w := range want {
		if changes[i].Kind != w {
			t.Errorf("changes[%d].Kind = %q, want %q", i, changes[i].Kind, w)
		}
	}
}

func TestIdentityAndResourceGroupPruningRespectsPolicy(t *testing.T) {
	bundle := &Bundle{
		Groups:         []SecurityGroup{{Name: "wanted-group"}},
		Apps:           []AppRegistration{{Name: "wanted-app"}},
		ResourceGroups: []ResourceGroup{{Name: "wanted-resources"}},
	}
	newProvider := func() *fakeProvider {
		return &fakeProvider{
			groupNames: []string{"wanted-group", "stale-group"},
			appNames:   []string{"wanted-app", "stale-app"},
			resourceGroups: []ResourceGroup{
				{Name: "wanted-resources"},
				{Name: "stale-resources"},
			},
			groups: []SecurityGroup{{Name: "wanted-group"}},
			apps:   []AppRegistration{{Name: "wanted-app"}},
		}
	}

	changes, err := Plan(newProvider(), bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, c := range changes {
		if c.Action == ActionDelete {
			t.Errorf("unexpected delete without prune enabled: %+v", c)
		}
	}

	changes, err = Plan(newProvider(), bundle, &Options{PruneIdentities: true, PruneResourceGroups: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var deleted []string
	for _, c := range changes {
		if c.Action == ActionDelete {
			deleted = append(deleted, c.Key)
		}
	}
	want := []string{"resourceGroup|stale-resources", "securityGroup|stale-group", "appRegistration|stale-app"}
	if len(deleted) != len(want) {
		t.Fatalf("deleted = %v, want %v", deleted, want)
	}
	for i, w := range want {
		if deleted[i] != w {
			t.Errorf("deleted[%d] = %q, want %q", i, deleted[i], w)
		}
	}
}

func TestRoleDefinitionScopeComparisonDetectsOnlyRealDrift(t *testing.T) {
	subscription := "00000000-0000-0000-0000-000000000001"
	resourceScope := Scope{Subscription: subscription, ResourceGroup: "resources"}
	desired := RoleDefinition{
		Name:             "reader",
		Description:      "synthetic role",
		AssignableScopes: []Scope{{Subscription: subscription}, resourceScope},
		Actions:          []string{"Microsoft.Network/dnsZones/read"},
	}
	bundle := &Bundle{RoleDefinitions: []RoleDefinition{desired}}

	drifted := &fakeProvider{roleDefinitions: []RoleDefinition{{
		Name:             "reader",
		Description:      "synthetic role",
		AssignableScopes: []Scope{{Subscription: "00000000-0000-0000-0000-000000000002"}},
		Actions:          []string{"Microsoft.Network/dnsZones/read"},
	}}}
	changes, err := Plan(drifted, bundle, &Options{Subscription: subscription})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Action != ActionUpdate {
		t.Fatalf("changes = %+v, want exactly one Update", changes)
	}

	equivalent := &fakeProvider{roleDefinitions: []RoleDefinition{{
		Name:             "reader",
		Description:      "synthetic role",
		AssignableScopes: []Scope{resourceScope, {Subscription: subscription}, resourceScope},
		Actions:          []string{"Microsoft.Network/dnsZones/read"},
	}}}
	changes, err = Plan(equivalent, bundle, &Options{Subscription: subscription})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none (scope sets are equivalent)", changes)
	}
}

func TestResourceGroupUpdatesMergeDeclaredTagsOnlyWhenNeeded(t *testing.T) {
	bundle := &Bundle{ResourceGroups: []ResourceGroup{{Name: "resources", Location: "eastus", Tags: map[string]string{"managed": "new"}}}}
	provider := &fakeProvider{resourceGroups: []ResourceGroup{{Name: "resources", Location: "eastus", Tags: map[string]string{"managed": "old", "preserved": "live"}}}}
	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want exactly one", changes)
	}
	updated := changes[0].Target.ResourceGroup
	if updated.Tags["managed"] != "new" {
		t.Errorf("managed tag = %q, want %q", updated.Tags["managed"], "new")
	}
	if updated.Tags["preserved"] != "live" {
		t.Errorf("preserved tag = %q, want %q", updated.Tags["preserved"], "live")
	}

	satisfied := &fakeProvider{resourceGroups: []ResourceGroup{{Name: "resources", Location: "eastus", Tags: map[string]string{"managed": "new", "preserved": "live"}}}}
	changes, err = Plan(satisfied, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none", changes)
	}
}

// applyFailProvider fails every group mutation to exercise a mid-run
// Apply failure.
type applyFailProvider struct{ fakeProvider }

func (p *applyFailProvider) PutGroup(SecurityGroup) error {
	return errors.New("synthetic group failure")
}

func TestApplyReportsEachSuccessfulChangeInOrder(t *testing.T) {
	changes := []Change{
		{Kind: "dnsRecordSet", Action: ActionCreate, Key: "a", Target: Target{Kind: TargetDns}},
		{Kind: "dnsRecordSet", Action: ActionUpdate, Key: "b", Target: Target{Kind: TargetDns}},
		{Kind: "dnsRecordSet", Action: ActionDelete, Key: "c", Target: Target{Kind: TargetDns}},
	}
	var seen []string
	if err := Apply(&fakeProvider{}, changes, &Options{Subscription: "sub"}, func(c Change) { seen = append(seen, c.Key) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(seen, want) {
		t.Errorf("reported keys = %v, want %v", seen, want)
	}
	if err := Apply(&fakeProvider{}, changes, &Options{Subscription: "sub"}, nil); err != nil {
		t.Errorf("Apply with nil reporter: %v", err)
	}
}

func TestApplyMidRunFailureReportsOnlyAppliedChanges(t *testing.T) {
	changes := []Change{
		{Kind: "dnsRecordSet", Action: ActionCreate, Key: "a", Target: Target{Kind: TargetDns}},
		{Kind: "securityGroup", Action: ActionCreate, Key: "b", Target: Target{Kind: TargetGroup, Group: SecurityGroup{Name: "g"}}},
		{Kind: "dnsRecordSet", Action: ActionCreate, Key: "c", Target: Target{Kind: TargetDns}},
	}
	var seen []string
	err := Apply(&applyFailProvider{}, changes, &Options{Subscription: "sub"}, func(c Change) { seen = append(seen, c.Key) })
	if err == nil {
		t.Fatal("Apply: expected mid-run failure, got nil error")
	}
	if want := []string{"a"}; !slices.Equal(seen, want) {
		t.Errorf("reported keys = %v, want only changes applied before the failure %v", seen, want)
	}
}

func TestPlanMissingZonePlansZoneCreateBeforeItsRecords(t *testing.T) {
	provider := &fakeProvider{zones: []string{"exists.com"}}
	bundle := &Bundle{Dns: []DnsRecord{
		{Zone: "new.com", RecordType: "A", Name: "www", TTL: 300, Values: []string{"192.0.2.1"}},
		{Zone: "exists.com", RecordType: "A", Name: "www", TTL: 300, Values: []string{"192.0.2.2"}},
	}}
	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %+v, want 3 (existing-zone record, zone create, new-zone record)", changes)
	}
	if changes[0].Kind != "dnsRecordSet" || changes[0].Target.Dns.Zone != "exists.com" {
		t.Errorf("changes[0] = %+v, want exists.com record create with no zone change", changes[0])
	}
	zone := changes[1]
	if zone.Kind != "dnsZone" || zone.Action != ActionCreate || zone.Key != "dnsZone|new.com" ||
		zone.Target.Kind != TargetZone || zone.Target.Zone != "new.com" {
		t.Errorf("changes[1] = %+v, want dnsZone|new.com create", zone)
	}
	if changes[2].Kind != "dnsRecordSet" || changes[2].Action != ActionCreate || changes[2].Target.Dns.Zone != "new.com" {
		t.Errorf("changes[2] = %+v, want new.com record create after its zone create", changes[2])
	}
	if want := []string{"exists.com"}; !slices.Equal(provider.listed, want) {
		t.Errorf("ListDNS calls = %v, want %v (missing zone must not be listed)", provider.listed, want)
	}
}

func TestApplyCreatesOnlyPlannedMissingZones(t *testing.T) {
	provider := &fakeProvider{}
	changes := []Change{
		{Kind: "dnsZone", Action: ActionCreate, Key: "dnsZone|new.com", Target: Target{Kind: TargetZone, Zone: "new.com"}},
		{Kind: "dnsRecordSet", Action: ActionCreate, Key: "www", Target: Target{Kind: TargetDns}},
	}
	var seen []string
	if err := Apply(provider, changes, &Options{Subscription: "sub"}, func(c Change) { seen = append(seen, c.Key) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"new.com"}; !slices.Equal(provider.ensured, want) {
		t.Errorf("EnsureZone calls = %v, want %v", provider.ensured, want)
	}
	if want := []string{"dnsZone|new.com", "www"}; !slices.Equal(seen, want) {
		t.Errorf("reported keys = %v, want %v", seen, want)
	}
	noZones := &fakeProvider{}
	if err := Apply(noZones, changes[1:], &Options{Subscription: "sub"}, nil); err != nil {
		t.Fatalf("Apply without zone changes: %v", err)
	}
	if len(noZones.ensured) != 0 {
		t.Errorf("EnsureZone calls = %v, want none for existing zones", noZones.ensured)
	}
}

func TestResourceGroupUpdateCarriesFieldDiffs(t *testing.T) {
	provider := &fakeProvider{resourceGroups: []ResourceGroup{{
		Name: "rg", Location: "eastus", Tags: map[string]string{"config_version": "0.13.0"},
	}}}
	bundle := &Bundle{ResourceGroups: []ResourceGroup{{
		Name: "rg", Location: "eastus", Tags: map[string]string{"config_version": "0.14.0"},
	}}}
	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Action != ActionUpdate {
		t.Fatalf("changes = %+v, want one update", changes)
	}
	want := []FieldDiff{{Field: "tag config_version", Old: "0.13.0", New: "0.14.0"}}
	if !slices.Equal(changes[0].Diffs, want) {
		t.Errorf("Diffs = %+v, want %+v", changes[0].Diffs, want)
	}
}

func TestLocationFormattingAloneIsNotDrift(t *testing.T) {
	provider := &fakeProvider{resourceGroups: []ResourceGroup{{Name: "rg", Location: "eastus"}}}
	sameRegion := &Bundle{ResourceGroups: []ResourceGroup{{Name: "rg", Location: "East US"}}}
	changes, err := Plan(provider, sameRegion, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("changes = %+v, want none for formatting-only location difference", changes)
	}
	otherRegion := &Bundle{ResourceGroups: []ResourceGroup{{Name: "rg", Location: "westus2"}}}
	changes, err = Plan(provider, otherRegion, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("changes = %+v, want one update for a genuinely different region", changes)
	}
	want := []FieldDiff{{Field: "location", Old: "eastus", New: "westus2"}}
	if !slices.Equal(changes[0].Diffs, want) {
		t.Errorf("Diffs = %+v, want %+v", changes[0].Diffs, want)
	}
}

func TestDnsUpdateCarriesFieldDiffs(t *testing.T) {
	provider := &fakeProvider{dns: []DnsRecord{
		{Zone: "example.com", RecordType: "A", Name: "www", TTL: 300, Values: []string{"192.0.2.1"}},
	}}
	bundle := &Bundle{Dns: []DnsRecord{
		{Zone: "example.com", RecordType: "A", Name: "www", TTL: 600, Values: []string{"192.0.2.2"}},
	}}
	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Action != ActionUpdate {
		t.Fatalf("changes = %+v, want one update", changes)
	}
	want := []FieldDiff{
		{Field: "ttl", Old: "300", New: "600"},
		{Field: "values", Old: "192.0.2.1", New: "192.0.2.2"},
	}
	if !slices.Equal(changes[0].Diffs, want) {
		t.Errorf("Diffs = %+v, want %+v", changes[0].Diffs, want)
	}
}

func TestGroupUpdateCarriesAddedAndRemovedMembers(t *testing.T) {
	provider := &fakeProvider{groups: []SecurityGroup{{Name: "eng", Members: []string{"alice", "bob"}}}}
	bundle := &Bundle{Groups: []SecurityGroup{{Name: "eng", Members: []string{"alice", "carol"}}}}
	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 1 || changes[0].Action != ActionUpdate {
		t.Fatalf("changes = %+v, want one update", changes)
	}
	want := []FieldDiff{
		{Field: "members added", New: "carol"},
		{Field: "members removed", Old: "bob"},
	}
	if !slices.Equal(changes[0].Diffs, want) {
		t.Errorf("Diffs = %+v, want %+v", changes[0].Diffs, want)
	}
}

// TestPlanRejectsKindTheProviderLacks — AT3: Plan names the provider and
// kind when handed a bundle the provider's capabilities cannot hold, and
// still plans the kinds it can.
func TestPlanRejectsKindTheProviderLacks(t *testing.T) {
	provider := &dnsOnlyProvider{}
	bundle := &Bundle{Groups: []SecurityGroup{{Name: "eng"}}}
	_, err := Plan(provider, bundle, &Options{Provider: "dnsonly"})
	if err == nil || err.Error() != `provider "dnsonly" does not support kind "securityGroup"` {
		t.Fatalf("Plan with an unsupported kind: %v", err)
	}
	dns := &Bundle{Dns: []DnsRecord{{Zone: "example.com", RecordType: "A", Name: "www", TTL: 300, Values: []string{"192.0.2.10"}}}}
	changes, err := Plan(provider, dns, &Options{Provider: "dnsonly"})
	if err != nil || len(changes) != 1 || changes[0].Kind != "dnsRecordSet" {
		t.Fatalf("Plan with a supported kind: changes=%v err=%v", changes, err)
	}
	applyErr := Apply(provider, []Change{{Kind: "securityGroup", Action: ActionCreate, Target: Target{Kind: TargetGroup, Group: SecurityGroup{Name: "eng"}}}}, &Options{}, nil)
	if applyErr == nil || !strings.Contains(applyErr.Error(), "lacks the identity capability") {
		t.Fatalf("Apply with an unsupported target: %v", applyErr)
	}
}

// TestHostKindsNeedTheirCapabilities — a provider without compute, network,
// storage, or IAM role capabilities fails by kind and never reaches apply.
func TestHostKindsNeedTheirCapabilities(t *testing.T) {
	provider := &dnsOnlyProvider{}
	for kind, bundle := range map[string]*Bundle{
		"iamRole":       {IamRoles: []IamRole{{Name: "r"}}},
		"bucket":        {Buckets: []Bucket{{Name: "b"}}},
		"firewall":      {Firewalls: []Firewall{{Name: "f"}}},
		"instance":      {Instances: []Instance{{Name: "i"}}},
		"staticAddress": {Addresses: []StaticAddress{{Name: "a"}}},
	} {
		_, err := Plan(provider, bundle, &Options{Provider: "dnsonly"})
		if err == nil || err.Error() != `provider "dnsonly" does not support kind "`+kind+`"` {
			t.Errorf("%s: %v", kind, err)
		}
	}
	err := Apply(provider, []Change{{Kind: "bucket", Action: ActionCreate, Target: Target{Kind: TargetBucket}}}, &Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "lacks the storage capability") {
		t.Errorf("Apply bucket: %v", err)
	}
}

func TestMonitoringKindsNeedTheirCapability(t *testing.T) {
	provider := &dnsOnlyProvider{}
	for kind, bundle := range map[string]*Bundle{
		"budget":      {Budgets: []Budget{{Name: "b"}}},
		"healthCheck": {HealthChecks: []HealthCheck{{Name: "h", Target: "192.0.2.1"}}},
	} {
		_, err := Plan(provider, bundle, &Options{Provider: "dnsonly"})
		if err == nil || err.Error() != `provider "dnsonly" does not support kind "`+kind+`"` {
			t.Errorf("%s: %v", kind, err)
		}
	}
}

// TestAppRegistrationSignInFieldsPlanOnlyWhenSet — AT2: a differing
// managed field plans one update with named diffs, redirect URIs compare
// as a set, and an unset field never plans against live values.
//
//go:fix inline
func TestAppRegistrationSignInFieldsPlanOnlyWhenSet(t *testing.T) {
	live := AppRegistration{Name: "site", SignInAudience: "AzureADMultipleOrgs", RedirectURIs: []string{"https://b/", "https://a/"}, IDTokenIssuance: new(true), Secrets: []Secret{{Name: "old", KeyID: "k1"}}}
	provider := &fakeProvider{apps: []AppRegistration{live}}

	bare := &Bundle{Apps: []AppRegistration{{Name: "site"}}}
	changes, err := Plan(provider, bare, &Options{PruneIdentities: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(changes) != 0 {
		t.Errorf("unset fields planned %+v", changes)
	}

	same := &Bundle{Apps: []AppRegistration{{Name: "site", SignInAudience: "AzureADMultipleOrgs", RedirectURIs: []string{"https://a/", "https://b/"}, IDTokenIssuance: new(true)}}}
	if changes, _ = Plan(provider, same, &Options{}); len(changes) != 0 {
		t.Errorf("matching fields planned %+v", changes)
	}

	differ := &Bundle{Apps: []AppRegistration{{Name: "site", SignInAudience: "AzureADMyOrg", RedirectURIs: []string{"https://a/"}, IDTokenIssuance: new(false)}}}
	changes, _ = Plan(provider, differ, &Options{})
	if len(changes) != 1 || changes[0].Action != ActionUpdate || changes[0].Summary != "sign-in settings differ" {
		t.Fatalf("changes = %+v", changes)
	}
	var fields []string
	for _, d := range changes[0].Diffs {
		fields = append(fields, d.Field)
	}
	for _, want := range []string{"signInAudience", "redirectUris removed", "idTokenIssuance"} {
		if !slices.Contains(fields, want) {
			t.Errorf("diffs %v lack %s", fields, want)
		}
	}
}

// TestAppRegistrationSecretsPlanCreateRotateAndDelete — AT3: a missing
// secret plans a create under its own key, an expired or pre-issuedAfter
// one plans a rotation that removes the old key, an unlisted one deletes
// only with secrets set and identity pruning on, and no plan line or diff
// carries a secret value.
func TestAppRegistrationSecretsPlanCreateRotateAndDelete(t *testing.T) {
	restore := now
	now = func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	defer func() { now = restore }()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 0, 0, 0, 0, time.UTC) }
	live := AppRegistration{Name: "site", Secrets: []Secret{
		{Name: "fresh", KeyID: "k-fresh", Issued: day(20), Expires: day(30)},
		{Name: "expired", KeyID: "k-expired", Issued: day(1), Expires: day(26)},
		{Name: "leaked", KeyID: "k-leaked", Issued: day(5), Expires: day(30)},
		{Name: "extra", KeyID: "k-extra", Issued: day(5), Expires: day(30)},
	}}
	provider := &fakeProvider{apps: []AppRegistration{live}}
	bundle := &Bundle{Apps: []AppRegistration{{Name: "site", Secrets: []Secret{
		{Name: "fresh", ValidForDays: 730},
		{Name: "expired", ValidForDays: 730},
		{Name: "leaked", ValidForDays: 730, IssuedAfter: day(10)},
		{Name: "new", ValidForDays: 30},
	}}}}

	changes, err := Plan(provider, bundle, &Options{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := map[string]Change{}
	for _, c := range changes {
		got[c.Key] = c
	}
	if len(changes) != 3 {
		t.Fatalf("changes = %+v", changes)
	}
	if c := got["appRegistration|site secret new"]; c.Action != ActionCreate || c.Kind != "appRegistration" {
		t.Errorf("new secret: %+v", c)
	}
	if c := got["appRegistration|site secret expired"]; c.Action != ActionUpdate || c.Summary != "rotate: expired" || len(c.Target.Stale) != 1 || c.Target.Stale[0] != "k-expired" {
		t.Errorf("expired secret: %+v", c)
	}
	if c := got["appRegistration|site secret leaked"]; c.Action != ActionUpdate || c.Summary != "rotate: issued before 2026-10-10T00:00:00Z" || len(c.Target.Stale) != 1 || c.Target.Stale[0] != "k-leaked" {
		t.Errorf("leaked secret: %+v", c)
	}

	changes, _ = Plan(provider, bundle, &Options{PruneIdentities: true})
	var deleted []string
	for _, c := range changes {
		if c.Action == ActionDelete {
			deleted = append(deleted, c.Key)
		}
	}
	if len(deleted) != 1 || deleted[0] != "appRegistration|site secret extra" {
		t.Errorf("deleted = %v", deleted)
	}
	unmanaged := &Bundle{Apps: []AppRegistration{{Name: "site"}}}
	if changes, _ = Plan(provider, unmanaged, &Options{PruneIdentities: true}); len(changes) != 0 {
		t.Errorf("omitted secrets planned %+v", changes)
	}

	if err := Apply(provider, changes, &Options{}, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	changes, _ = Plan(provider, bundle, &Options{PruneIdentities: true})
	var reported []Change
	if err := Apply(provider, changes, &Options{}, func(c Change) { reported = append(reported, c) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(provider.secretsAdded) != 3 || len(provider.secretsRemoved) != 3 {
		t.Errorf("added %v removed %v", provider.secretsAdded, provider.secretsRemoved)
	}
	shown := 0
	for _, c := range reported {
		if strings.Contains(c.Summary, "synthetic-secret-value") {
			shown++
		}
	}
	if shown != 3 {
		t.Errorf("secret value shown on %d applied lines, want 3", shown)
	}
	for _, c := range changes {
		if strings.Contains(c.Summary, "synthetic") {
			t.Errorf("plan line carries a value: %+v", c)
		}
		for _, d := range c.Diffs {
			if strings.Contains(d.Old+d.New, "synthetic") {
				t.Errorf("diff carries a value: %+v", d)
			}
		}
	}
}

// TestAppRegistrationTenantKeysAndPrunesPerTenant — AT5: a spec naming
// another tenant is looked up and keyed there, and identity pruning lists
// each named tenant as well as the signed-in one.
func TestAppRegistrationTenantKeysAndPrunesPerTenant(t *testing.T) {
	other := "22222222-2222-2222-2222-222222222222"
	bundle := &Bundle{Apps: []AppRegistration{{Name: "consumer", Tenant: other}, {Name: "home"}}}
	provider := &fakeProvider{apps: []AppRegistration{{Name: "home"}}, appNames: []string{"home", "consumer", "stale"}}
	changes, err := Plan(provider, bundle, &Options{PruneIdentities: true})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	var keys []string
	for _, c := range changes {
		keys = append(keys, pastTense(c.Action)+" "+c.Key)
	}
	want := []string{
		"created appRegistration|consumer@" + other,
		"deleted appRegistration|consumer",
		"deleted appRegistration|stale",
		"deleted appRegistration|home@" + other,
		"deleted appRegistration|stale@" + other,
	}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// fakeSink is a provider that receives secret deliveries, recording each
// value it is given and counting how often it is grounded.
type fakeSink struct {
	fakeProvider
	grounds    int
	failGround bool
	targets    map[string]bool
	delivered  map[string]string
	deliverErr error
}

func (s *fakeSink) Ground() (Account, error) {
	s.grounds++
	if s.failGround {
		return Account{}, errors.New("sink unreachable")
	}
	return s.fakeProvider.Ground()
}

func (s *fakeSink) HasSecretTarget(target string) (bool, error) { return s.targets[target], nil }

func (s *fakeSink) DeliverSecret(target string, value []byte) error {
	if s.deliverErr != nil {
		return s.deliverErr
	}
	if s.delivered == nil {
		s.delivered = map[string]string{}
	}
	s.delivered[target] = string(value)
	return nil
}

func deliveredSecret(name, target string) Secret {
	return Secret{Name: name, ValidForDays: 730, DeliverTo: &Delivery{Provider: "aws", Field: "parameter", Target: target}}
}

// TestDeliveredSecretPlansOnTargetPresence — AT2: a missing delivered
// secret plans a create naming the target, an existing one whose target is
// missing plans a rotation, one whose target exists plans nothing, and no
// line or diff carries a value.
func TestDeliveredSecretPlansOnTargetPresence(t *testing.T) {
	restore := now
	now = func() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }
	defer func() { now = restore }()
	future := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	live := AppRegistration{Name: "site", Secrets: []Secret{
		{Name: "present", KeyID: "k-present", Issued: now().AddDate(0, -1, 0), Expires: future},
		{Name: "absent", KeyID: "k-absent", Issued: now().AddDate(0, -1, 0), Expires: future},
	}}
	provider := &fakeProvider{apps: []AppRegistration{live}}
	sink := &fakeSink{targets: map[string]bool{"/app/present": true}}
	bundle := &Bundle{Apps: []AppRegistration{{Name: "site", Secrets: []Secret{
		deliveredSecret("present", "/app/present"),
		deliveredSecret("absent", "/app/absent"),
		deliveredSecret("new", "/app/new"),
	}}}}
	changes, err := Plan(provider, bundle, &Options{Sinks: map[string]SecretSink{"aws": sink}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := map[string]string{}
	for _, c := range changes {
		got[c.Key] = c.Summary
		for _, d := range c.Diffs {
			if strings.Contains(d.Old+d.New, "synthetic") {
				t.Errorf("diff carries a value: %+v", d)
			}
		}
	}
	want := map[string]string{
		"appRegistration|site secret absent": "rotate: aws parameter /app/absent missing",
		"appRegistration|site secret new":    "missing; deliver to aws parameter /app/new",
	}
	if len(got) != len(want) {
		t.Fatalf("changes = %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}

	expired := AppRegistration{Name: "site", Secrets: []Secret{{Name: "absent", KeyID: "k-absent", Issued: now().AddDate(-1, 0, 0), Expires: now().AddDate(0, 0, -1)}}}
	changes, _ = Plan(&fakeProvider{apps: []AppRegistration{expired}}, &Bundle{Apps: []AppRegistration{{Name: "site", Secrets: []Secret{deliveredSecret("absent", "/app/absent")}}}}, &Options{Sinks: map[string]SecretSink{"aws": sink}})
	if len(changes) != 1 || changes[0].Summary != "rotate: expired" {
		t.Errorf("expired and missing target: %+v", changes)
	}

	if _, err := Plan(provider, bundle, &Options{}); err == nil || !strings.Contains(err.Error(), "not grounded for delivery") {
		t.Errorf("no sink: got %v", err)
	}
}

// TestDeliveredSecretAppliesAndRollsBack — AT3: apply adds the credential,
// delivers the exact value, then removes the stale one, and its line names
// the target without the value; a failed delivery removes only the new
// credential and returns the rollback error.
func TestDeliveredSecretAppliesAndRollsBack(t *testing.T) {
	secret := deliveredSecret("host", "/app/host")
	rotation := []Change{{Kind: "appRegistration", Action: ActionUpdate, Key: "appRegistration|site secret host", Summary: "rotate: expired",
		Target: Target{Kind: TargetAppSecret, App: AppRegistration{Name: "site"}, Secret: secret, Stale: []string{"k-old"}}}}

	provider := &fakeProvider{}
	sink := &fakeSink{}
	var reported []Change
	if err := Apply(provider, rotation, &Options{Sinks: map[string]SecretSink{"aws": sink}}, func(c Change) { reported = append(reported, c) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if sink.delivered["/app/host"] != "synthetic-secret-value" {
		t.Errorf("delivered = %v", sink.delivered)
	}
	if !slices.Equal(provider.secretsRemoved, []string{"k-old"}) {
		t.Errorf("removed = %v", provider.secretsRemoved)
	}
	if len(reported) != 1 || reported[0].Summary != "value delivered to aws parameter /app/host" {
		t.Errorf("reported = %+v", reported)
	}

	provider = &fakeProvider{}
	failing := &fakeSink{deliverErr: errors.New("aws ssm put-parameter failed with exit 255")}
	err := Apply(provider, rotation, &Options{Sinks: map[string]SecretSink{"aws": failing}}, nil)
	want := "deliver secret host to aws parameter /app/host: aws ssm put-parameter failed with exit 255; the new credential was removed and the old one is unchanged"
	if err == nil || err.Error() != want {
		t.Errorf("Apply = %v, want %q", err, want)
	}
	if !slices.Equal(provider.secretsRemoved, []string{"k-new-host"}) {
		t.Errorf("rollback removed %v, want only the new credential", provider.secretsRemoved)
	}
	if err != nil && strings.Contains(err.Error(), "synthetic") {
		t.Errorf("error carries the value: %v", err)
	}
}
