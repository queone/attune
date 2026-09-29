package main

import (
	"errors"
	"slices"
	"testing"
)

// fakeProvider is a hand-rolled Provider double for Plan/Apply tests,
// mirroring rkit's AssignmentProvider test fixture.
type fakeProvider struct {
	assignments     []LiveAssignment
	dns             []DnsRecord
	zones           []string
	ensured         []string
	listed          []string
	groups          []SecurityGroup
	groupNames      []string
	apps            []AppRegistration
	appNames        []string
	roleDefinitions []RoleDefinition
	resourceGroups  []ResourceGroup
}

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
func (p *fakeProvider) PutDNS(DnsRecord) error            { return nil }
func (p *fakeProvider) DeleteDNS(DnsRecord) error         { return nil }
func (p *fakeProvider) ListGroupNames() ([]string, error) { return p.groupNames, nil }
func (p *fakeProvider) PutGroup(SecurityGroup) error      { return nil }
func (p *fakeProvider) DeleteGroup(string) error          { return nil }
func (p *fakeProvider) ListAppNames() ([]string, error)   { return p.appNames, nil }
func (p *fakeProvider) PutApp(AppRegistration) error      { return nil }
func (p *fakeProvider) DeleteApp(string) error            { return nil }
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

func (p *fakeProvider) GetApp(name string) (*AppRegistration, error) {
	for _, a := range p.apps {
		if a.Name == name {
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
	if err := Apply(&fakeProvider{}, changes, "sub", func(c Change) { seen = append(seen, c.Key) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(seen, want) {
		t.Errorf("reported keys = %v, want %v", seen, want)
	}
	if err := Apply(&fakeProvider{}, changes, "sub", nil); err != nil {
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
	err := Apply(&applyFailProvider{}, changes, "sub", func(c Change) { seen = append(seen, c.Key) })
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
	if err := Apply(provider, changes, "sub", func(c Change) { seen = append(seen, c.Key) }); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := []string{"new.com"}; !slices.Equal(provider.ensured, want) {
		t.Errorf("EnsureZone calls = %v, want %v", provider.ensured, want)
	}
	if want := []string{"dnsZone|new.com", "www"}; !slices.Equal(seen, want) {
		t.Errorf("reported keys = %v, want %v", seen, want)
	}
	noZones := &fakeProvider{}
	if err := Apply(noZones, changes[1:], "sub", nil); err != nil {
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
