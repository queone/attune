package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Action is the reconciliation action for one Change.
type Action int

const (
	ActionCreate Action = iota
	ActionUpdate
	ActionDelete
	// ActionReplace marks a change to an immutable field. Plan reports it
	// and Apply refuses it; the Director removes and re-adds the spec.
	ActionReplace
)

func (a Action) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
	case ActionReplace:
		return "replace"
	default:
		return "unknown"
	}
}

// TargetKind discriminates which field of Target is populated.
type TargetKind int

const (
	TargetDns TargetKind = iota
	TargetZone
	TargetGroup
	TargetApp
	TargetAppSecret
	TargetRoleDefinition
	TargetRoleAssignment
	TargetResourceGroup
	TargetIamRole
	TargetBucket
	TargetFirewall
	TargetInstance
	TargetAddress
	TargetBudget
	TargetHealthCheck
)

// RoleAssignmentTarget carries a role-assignment change's resolved identity.
type RoleAssignmentTarget struct {
	Desired     RoleAssignment
	PrincipalID string
	RoleID      string
	ScopeID     string
}

// Target is the resource a Change applies to, tagged by Kind.
type Target struct {
	Kind           TargetKind
	Zone           string
	Dns            DnsRecord
	Group          SecurityGroup
	App            AppRegistration
	Secret         Secret
	Stale          []string
	RoleDefinition RoleDefinition
	RoleAssignment RoleAssignmentTarget
	ResourceGroup  ResourceGroup
	IamRole        IamRole
	Bucket         Bucket
	Firewall       Firewall
	Instance       Instance
	Address        StaticAddress
	Budget         Budget
	HealthCheck    HealthCheck
}

// Change is one planned create/update/delete action. Diffs carries the
// field-level differences behind an update, shown only under -V/--verbose.
type Change struct {
	Kind    string
	Action  Action
	Key     string
	Summary string
	Diffs   []FieldDiff
	Target  Target
}

// Options controls which kinds Plan considers and which prune policies
// apply. Provider names the provider for error messages.
type Options struct {
	Provider            string
	Subscription        string
	Kind                string
	PruneDNS            bool
	PruneIdentities     bool
	PruneRoles          bool
	PruneResourceGroups bool
	PruneHosts          bool
	PruneMonitoring     bool
	// Outputs collects live outputs during Plan and applied outputs during
	// Apply, so a record value can reference another spec. Created when nil.
	Outputs *Outputs
}

// outputs returns the run's output set, creating it on first use.
func (o *Options) outputs() *Outputs {
	if o.Outputs == nil {
		o.Outputs = NewOutputs()
	}
	return o.Outputs
}

// LiveAssignment is a role assignment as read from the live provider.
type LiveAssignment struct {
	ID          string
	PrincipalID string
	RoleID      string
	Scope       string
}

func wanted(options *Options, kind string) bool {
	return options.Kind == "" || options.Kind == kind
}

func sortedCopy(values []string) []string {
	return normalized(values)
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func change(kind string, action Action, key, summary string, target Target) Change {
	return Change{Kind: kind, Action: action, Key: key, Summary: summary, Target: target}
}

// setDiffs renders a set-valued field's membership change as added and
// removed entries.
func setDiffs(field string, live, desired []string) []FieldDiff {
	liveSet := map[string]bool{}
	for _, v := range live {
		liveSet[v] = true
	}
	desiredSet := map[string]bool{}
	for _, v := range desired {
		desiredSet[v] = true
	}
	var added, removed []string
	for _, v := range sortedCopy(desired) {
		if !liveSet[v] {
			added = append(added, v)
		}
	}
	for _, v := range sortedCopy(live) {
		if !desiredSet[v] {
			removed = append(removed, v)
		}
	}
	var diffs []FieldDiff
	if len(added) > 0 {
		diffs = append(diffs, FieldDiff{Field: field + " added", New: strings.Join(added, ", ")})
	}
	if len(removed) > 0 {
		diffs = append(diffs, FieldDiff{Field: field + " removed", Old: strings.Join(removed, ", ")})
	}
	return diffs
}

// resourceGroupDiffs lists the differences driving a resource-group update.
func resourceGroupDiffs(current, desired ResourceGroup) []FieldDiff {
	var diffs []FieldDiff
	if !sameLocation(current.Location, desired.Location) {
		diffs = append(diffs, FieldDiff{Field: "location", Old: current.Location, New: desired.Location})
	}
	var keys []string
	for k := range desired.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if current.Tags[k] != desired.Tags[k] {
			diffs = append(diffs, FieldDiff{Field: "tag " + k, Old: current.Tags[k], New: desired.Tags[k]})
		}
	}
	return diffs
}

// dnsRecordDiffs lists the differences driving a DNS record-set update.
func dnsRecordDiffs(current, desired DnsRecord) []FieldDiff {
	var diffs []FieldDiff
	if current.TTL != desired.TTL {
		diffs = append(diffs, FieldDiff{Field: "ttl", Old: fmt.Sprintf("%d", current.TTL), New: fmt.Sprintf("%d", desired.TTL)})
	}
	if !stringsEqual(sortedCopy(current.Values), sortedCopy(desired.Values)) {
		diffs = append(diffs, FieldDiff{Field: "values", Old: strings.Join(current.Values, ", "), New: strings.Join(desired.Values, ", ")})
	}
	return diffs
}

// roleDefinitionDiffs lists the differences driving a role-definition
// update, mirroring sameRoleDefinition's comparisons.
func roleDefinitionDiffs(current, desired RoleDefinition, subscription string) []FieldDiff {
	var diffs []FieldDiff
	if current.Description != desired.Description {
		diffs = append(diffs, FieldDiff{Field: "description", Old: current.Description, New: desired.Description})
	}
	diffs = append(diffs, setDiffs("actions", current.Actions, desired.Actions)...)
	diffs = append(diffs, setDiffs("notActions", current.NotActions, desired.NotActions)...)
	diffs = append(diffs, setDiffs("dataActions", current.DataActions, desired.DataActions)...)
	diffs = append(diffs, setDiffs("notDataActions", current.NotDataActions, desired.NotDataActions)...)
	currentScopes, currentErr := roleScopes(current, subscription)
	desiredScopes, desiredErr := roleScopes(desired, subscription)
	if currentErr == nil && desiredErr == nil {
		diffs = append(diffs, setDiffs("assignableScopes", currentScopes, desiredScopes)...)
	}
	return diffs
}

// Plan computes create/update/delete changes for bundle against provider's
// live state, honoring options.Kind filtering and prune policy. Kinds are
// always processed in this fixed order: resourceGroup, securityGroup,
// appRegistration, roleDefinition, roleAssignment, then the host kinds
// iamRole, bucket, firewall, instance, staticAddress with their prunes in
// reverse, then dnsRecordSet. Each kind family asserts its capability
// interface on provider and fails when the provider lacks it.
func Plan(provider LiveProvider, bundle *Bundle, options *Options) ([]Change, error) {
	var changes []Change

	if wanted(options, "resourceGroup") && len(bundle.ResourceGroups) > 0 {
		rgs, ok := provider.(ResourceGroupProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "resourceGroup")
		}
		live, err := rgs.ListResourceGroups()
		if err != nil {
			return nil, err
		}
		desired := map[string]bool{}
		for _, item := range bundle.ResourceGroups {
			desired[item.Name] = true
		}
		for _, item := range bundle.ResourceGroups {
			current, found := findResourceGroup(live, item.Name)
			switch {
			case !found:
				changes = append(changes, change("resourceGroup", ActionCreate, "resourceGroup|"+item.Name, "missing", Target{Kind: TargetResourceGroup, ResourceGroup: item}))
			case !sameLocation(current.Location, item.Location) || !tagsSatisfy(item.Tags, current.Tags):
				merged := map[string]string{}
				maps.Copy(merged, current.Tags)
				maps.Copy(merged, item.Tags)
				updated := item
				updated.Tags = merged
				c := change("resourceGroup", ActionUpdate, "resourceGroup|"+item.Name, "location or declared tags differ", Target{Kind: TargetResourceGroup, ResourceGroup: updated})
				c.Diffs = resourceGroupDiffs(current, item)
				changes = append(changes, c)
			}
		}
		if options.PruneResourceGroups {
			for _, item := range live {
				if !desired[item.Name] {
					changes = append(changes, change("resourceGroup", ActionDelete, "resourceGroup|"+item.Name, "absent from specs", Target{Kind: TargetResourceGroup, ResourceGroup: item}))
				}
			}
		}
	}

	if wanted(options, "securityGroup") && len(bundle.Groups) > 0 {
		identity, ok := provider.(IdentityProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "securityGroup")
		}
		for _, item := range bundle.Groups {
			current, err := identity.GetGroup(item.Name)
			if err != nil {
				return nil, err
			}
			switch {
			case current == nil:
				changes = append(changes, change("securityGroup", ActionCreate, "securityGroup|"+item.Name, "missing", Target{Kind: TargetGroup, Group: item}))
			case !stringsEqual(sortedCopy(current.Owners), sortedCopy(item.Owners)) || !stringsEqual(sortedCopy(current.Members), sortedCopy(item.Members)):
				c := change("securityGroup", ActionUpdate, "securityGroup|"+item.Name, "owners or members differ", Target{Kind: TargetGroup, Group: item})
				c.Diffs = append(setDiffs("owners", current.Owners, item.Owners), setDiffs("members", current.Members, item.Members)...)
				changes = append(changes, c)
			}
		}
		if options.PruneIdentities {
			desired := map[string]bool{}
			for _, item := range bundle.Groups {
				desired[item.Name] = true
			}
			names, err := identity.ListGroupNames()
			if err != nil {
				return nil, err
			}
			for _, name := range names {
				if !desired[name] {
					changes = append(changes, change("securityGroup", ActionDelete, "securityGroup|"+name, "absent from specs", Target{Kind: TargetGroup, Group: SecurityGroup{Name: name}}))
				}
			}
		}
	}

	if wanted(options, "appRegistration") && len(bundle.Apps) > 0 {
		identity, ok := provider.(IdentityProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "appRegistration")
		}
		for _, item := range bundle.Apps {
			current, err := identity.GetApp(item.Tenant, item.Name)
			if err != nil {
				return nil, err
			}
			changes = append(changes, planApp(current, item, options)...)
		}
		if options.PruneIdentities {
			desired := map[string]bool{}
			tenants := []string{""}
			for _, item := range bundle.Apps {
				desired[item.Key()] = true
				if item.Tenant != "" && !slices.Contains(tenants, item.Tenant) {
					tenants = append(tenants, item.Tenant)
				}
			}
			for _, tenant := range tenants {
				names, err := identity.ListAppNames(tenant)
				if err != nil {
					return nil, err
				}
				for _, name := range names {
					live := AppRegistration{Name: name, Tenant: tenant}
					if !desired[live.Key()] {
						changes = append(changes, change("appRegistration", ActionDelete, live.Key(), "absent from specs", Target{Kind: TargetApp, App: live}))
					}
				}
			}
		}
	}

	if wanted(options, "roleDefinition") && len(bundle.RoleDefinitions) > 0 {
		roles, ok := provider.(RoleProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "roleDefinition")
		}
		live, err := roles.ListRoleDefinitions(options.Subscription)
		if err != nil {
			return nil, err
		}
		desired := map[string]bool{}
		for _, item := range bundle.RoleDefinitions {
			desired[item.Name] = true
		}
		for _, item := range bundle.RoleDefinitions {
			current, found := findRoleDefinition(live, item.Name)
			switch {
			case !found:
				changes = append(changes, change("roleDefinition", ActionCreate, "roleDefinition|"+item.Name, "missing", Target{Kind: TargetRoleDefinition, RoleDefinition: item}))
			case !sameRoleDefinition(current, item, options.Subscription):
				c := change("roleDefinition", ActionUpdate, "roleDefinition|"+item.Name, "permissions or scopes differ", Target{Kind: TargetRoleDefinition, RoleDefinition: item})
				c.Diffs = roleDefinitionDiffs(current, item, options.Subscription)
				changes = append(changes, c)
			}
		}
		if options.PruneRoles {
			for _, item := range live {
				if !desired[item.Name] {
					changes = append(changes, change("roleDefinition", ActionDelete, "roleDefinition|"+item.Name, "absent from specs", Target{Kind: TargetRoleDefinition, RoleDefinition: item}))
				}
			}
		}
	}

	if wanted(options, "roleAssignment") && len(bundle.RoleAssignments) > 0 {
		roles, ok := provider.(RoleProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "roleAssignment")
		}
		if err := planRoleAssignments(roles, bundle, options, &changes); err != nil {
			return nil, err
		}
	}

	budgetDeletes, err := planBudgets(provider, bundle, options, &changes)
	if err != nil {
		return nil, err
	}
	if err := planHost(provider, bundle, options, &changes); err != nil {
		return nil, err
	}
	if (wanted(options, "dnsRecordSet") && len(bundle.Dns) > 0) || (wanted(options, "healthCheck") && len(bundle.HealthChecks) > 0) {
		if err := recordReferencedOutputs(provider, bundle, options); err != nil {
			return nil, err
		}
	}
	if err := planHealthChecks(provider, bundle, options, &changes); err != nil {
		return nil, err
	}
	changes = append(changes, budgetDeletes...)

	if wanted(options, "dnsRecordSet") && len(bundle.Dns) > 0 {
		dns, ok := provider.(DNSProvider)
		if !ok {
			return nil, unsupportedKind(options.Provider, "dnsRecordSet")
		}
		if err := planDNS(dns, bundle, options, &changes); err != nil {
			return nil, err
		}
	}

	return changes, nil
}

type desiredAssignment struct {
	item        RoleAssignment
	principalID string
	roleID      string
}

func planRoleAssignments(provider RoleProvider, bundle *Bundle, options *Options, changes *[]Change) error {
	desiredByScope := map[string][]desiredAssignment{}
	var scopeOrder []string
	for _, item := range bundle.RoleAssignments {
		principalID, err := provider.ResolvePrincipal(item.Principal, item.PrincipalType)
		if err != nil {
			return err
		}
		roleID, err := provider.ResolveRole(item.Role, options.Subscription)
		if err != nil {
			return err
		}
		scopeID, err := item.Scope.ArmID(options.Subscription)
		if err != nil {
			return err
		}
		if _, ok := desiredByScope[scopeID]; !ok {
			scopeOrder = append(scopeOrder, scopeID)
		}
		desiredByScope[scopeID] = append(desiredByScope[scopeID], desiredAssignment{item, principalID, roleID})
	}
	sort.Strings(scopeOrder)
	for _, scopeID := range scopeOrder {
		desired := desiredByScope[scopeID]
		live, err := provider.ListRoleAssignments(scopeID)
		if err != nil {
			return err
		}
		for _, d := range desired {
			found := false
			for _, candidate := range live {
				if eqID(candidate.PrincipalID, d.principalID) && eqID(candidate.RoleID, d.roleID) {
					found = true
					break
				}
			}
			if !found {
				*changes = append(*changes, change("roleAssignment", ActionCreate,
					fmt.Sprintf("roleAssignment|%s|%s|%s", d.item.Principal, d.item.Role, scopeID),
					"missing",
					Target{Kind: TargetRoleAssignment, RoleAssignment: RoleAssignmentTarget{Desired: d.item, PrincipalID: d.principalID, RoleID: d.roleID, ScopeID: scopeID}}))
			}
		}
		if options.PruneRoles {
			for _, assignment := range live {
				retained := false
				for _, d := range desired {
					if eqID(assignment.PrincipalID, d.principalID) && eqID(assignment.RoleID, d.roleID) {
						retained = true
						break
					}
				}
				if !retained {
					last := assignment.ID
					if idx := strings.LastIndex(assignment.ID, "/"); idx >= 0 {
						last = assignment.ID[idx+1:]
					}
					if last == "" {
						last = "unknown"
					}
					fallback := desired[0].item
					*changes = append(*changes, change("roleAssignment", ActionDelete,
						fmt.Sprintf("roleAssignment|%s", last),
						"absent from specs at managed scope",
						Target{Kind: TargetRoleAssignment, RoleAssignment: RoleAssignmentTarget{Desired: fallback, PrincipalID: assignment.PrincipalID, RoleID: assignment.RoleID, ScopeID: assignment.ID}}))
				}
			}
		}
	}
	return nil
}

func planDNS(provider DNSProvider, bundle *Bundle, options *Options, changes *[]Change) error {
	desiredByZone := map[string][]DnsRecord{}
	var zoneOrder []string
	for _, item := range bundle.Dns {
		if _, ok := desiredByZone[item.Zone]; !ok {
			zoneOrder = append(zoneOrder, item.Zone)
		}
		desiredByZone[item.Zone] = append(desiredByZone[item.Zone], item)
	}
	sort.Strings(zoneOrder)
	for _, zone := range zoneOrder {
		desired := desiredByZone[zone]
		exists, err := provider.HasZone(zone)
		if err != nil {
			return err
		}
		var live []DnsRecord
		if exists {
			live, err = provider.ListDNS(zone)
			if err != nil {
				return err
			}
		} else {
			*changes = append(*changes, change("dnsZone", ActionCreate, "dnsZone|"+zone, "missing", Target{Kind: TargetZone, Zone: zone}))
		}
		desiredKeys := map[string]bool{}
		for _, item := range desired {
			desiredKeys[item.Key()] = true
		}
		for _, item := range desired {
			resolved := item
			var pending []Reference
			resolved.Values, pending = options.outputs().Resolve(item.Values)
			current, found := findDNS(live, item.Key())
			switch {
			case !found && len(pending) > 0:
				*changes = append(*changes, change("dnsRecordSet", ActionCreate, item.Key(), pendingSummary(pending), Target{Kind: TargetDns, Dns: item}))
			case !found:
				*changes = append(*changes, change("dnsRecordSet", ActionCreate, item.Key(), "missing", Target{Kind: TargetDns, Dns: resolved}))
			case len(pending) > 0:
				c := change("dnsRecordSet", ActionUpdate, item.Key(), pendingSummary(pending), Target{Kind: TargetDns, Dns: item})
				c.Diffs = dnsRecordDiffs(current, resolved)
				*changes = append(*changes, c)
			case !current.SameData(resolved):
				c := change("dnsRecordSet", ActionUpdate, item.Key(), "TTL or values differ", Target{Kind: TargetDns, Dns: resolved})
				c.Diffs = dnsRecordDiffs(current, resolved)
				*changes = append(*changes, c)
			}
		}
		if options.PruneDNS {
			for _, item := range live {
				if !desiredKeys[item.Key()] && !dnsProtected(item) {
					*changes = append(*changes, change("dnsRecordSet", ActionDelete, item.Key(), "absent from specs", Target{Kind: TargetDns, Dns: item}))
				}
			}
		}
	}
	return nil
}

func findResourceGroup(live []ResourceGroup, name string) (ResourceGroup, bool) {
	for _, item := range live {
		if item.Name == name {
			return item, true
		}
	}
	return ResourceGroup{}, false
}

func findRoleDefinition(live []RoleDefinition, name string) (RoleDefinition, bool) {
	for _, item := range live {
		if item.Name == name {
			return item, true
		}
	}
	return RoleDefinition{}, false
}

func findDNS(live []DnsRecord, key string) (DnsRecord, bool) {
	for _, item := range live {
		if item.Key() == key {
			return item, true
		}
	}
	return DnsRecord{}, false
}

func eqID(left, right string) bool {
	if strings.EqualFold(left, right) {
		return true
	}
	leftTail := left
	if idx := strings.LastIndex(left, "/"); idx >= 0 {
		leftTail = left[idx+1:]
	}
	rightTail := right
	if idx := strings.LastIndex(right, "/"); idx >= 0 {
		rightTail = right[idx+1:]
	}
	return strings.EqualFold(leftTail, rightTail)
}

// roleScopes resolves a role definition's assignable scopes to ARM IDs,
// defaulting to the subscription scope when none are declared.
func roleScopes(role RoleDefinition, subscription string) ([]string, error) {
	if len(role.AssignableScopes) == 0 {
		return []string{fmt.Sprintf("/subscriptions/%s", subscription)}, nil
	}
	var out []string
	for _, s := range role.AssignableScopes {
		id, err := s.ArmID(subscription)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

func sameRoleDefinition(left, right RoleDefinition, subscription string) bool {
	leftScopes, leftErr := roleScopes(left, subscription)
	rightScopes, rightErr := roleScopes(right, subscription)
	if leftErr != nil || rightErr != nil {
		return false
	}
	if !stringsEqual(sortedCopy(leftScopes), sortedCopy(rightScopes)) {
		return false
	}
	return left.Description == right.Description &&
		stringsEqual(sortedCopy(left.Actions), sortedCopy(right.Actions)) &&
		stringsEqual(sortedCopy(left.NotActions), sortedCopy(right.NotActions)) &&
		stringsEqual(sortedCopy(left.DataActions), sortedCopy(right.DataActions)) &&
		stringsEqual(sortedCopy(left.NotDataActions), sortedCopy(right.NotDataActions))
}

// dnsProtected reports whether item is an apex (name "@") SOA or NS record,
// which Plan never schedules for deletion even when DNS pruning is enabled.
func dnsProtected(item DnsRecord) bool {
	if item.Name != "@" {
		return false
	}
	t := strings.ToUpper(item.RecordType)
	return t == "SOA" || t == "NS"
}

// Apply performs every planned Change against provider, in order,
// invoking report after each successful mutation when report is non-nil.
// It records each applied address and instance in options.Outputs and
// fills a record's references from them before the record is sent. A
// replace change is refused, since the field it names is immutable. Plan
// only emits changes for kinds the provider supports, so a missing
// capability here is a programming error reported by kind.
func Apply(provider LiveProvider, changes []Change, options *Options, report func(Change)) error {
	for i := range changes {
		c := changes[i]
		if c.Action == ActionReplace {
			return fmt.Errorf("replace %s %s: %s; remove the spec, apply, then add it back", c.Kind, c.Key, c.Summary)
		}
		if err := applyChange(provider, &c, options); err != nil {
			return err
		}
		if report != nil {
			report(c)
		}
	}
	return nil
}

// applyChange performs one Change through the capability its target needs.
// It may amend the change's summary with a result worth showing, such as
// the public address a staticAddress received.
func applyChange(provider LiveProvider, c *Change, options *Options) error {
	subscription := options.Subscription
	outputs := options.outputs()
	lacks := func(capability string) error {
		return fmt.Errorf("apply %s: provider lacks the %s capability", c.Kind, capability)
	}
	switch c.Target.Kind {
	case TargetIamRole:
		roles, ok := provider.(IamRoleProvider)
		if !ok {
			return lacks("IAM role")
		}
		if c.Action == ActionDelete {
			return roles.DeleteIamRole(c.Target.IamRole)
		}
		return roles.PutIamRole(c.Target.IamRole)
	case TargetBucket:
		storage, ok := provider.(StorageProvider)
		if !ok {
			return lacks("storage")
		}
		if c.Action == ActionDelete {
			return storage.DeleteBucket(c.Target.Bucket)
		}
		return storage.PutBucket(c.Target.Bucket)
	case TargetFirewall:
		network, ok := provider.(NetworkProvider)
		if !ok {
			return lacks("network")
		}
		if c.Action == ActionDelete {
			return network.DeleteFirewall(c.Target.Firewall)
		}
		return network.PutFirewall(c.Target.Firewall)
	case TargetBudget:
		monitoring, ok := provider.(MonitoringProvider)
		if !ok {
			return lacks("monitoring")
		}
		if c.Action == ActionDelete {
			return monitoring.DeleteBudget(c.Target.Budget)
		}
		return monitoring.PutBudget(c.Target.Budget)
	case TargetHealthCheck:
		monitoring, ok := provider.(MonitoringProvider)
		if !ok {
			return lacks("monitoring")
		}
		if c.Action == ActionDelete {
			return monitoring.DeleteHealthCheck(c.Target.HealthCheck)
		}
		check := c.Target.HealthCheck
		resolved, pending := outputs.Resolve([]string{check.Target})
		if len(pending) > 0 {
			return fmt.Errorf("reference %s is not resolved; apply its target first", pending[0])
		}
		check.Target = resolved[0]
		return monitoring.PutHealthCheck(check)
	case TargetInstance, TargetAddress:
		compute, ok := provider.(ComputeProvider)
		if !ok {
			return lacks("compute")
		}
		switch {
		case c.Target.Kind == TargetInstance && c.Action == ActionDelete:
			return compute.DeleteInstance(c.Target.Instance)
		case c.Target.Kind == TargetInstance:
			id, err := compute.PutInstance(c.Target.Instance)
			if err == nil {
				outputs.Record("instance", c.Target.Instance.Name, "id", id)
			}
			return err
		case c.Action == ActionDelete:
			return compute.DeleteAddress(c.Target.Address)
		default:
			ip, err := compute.PutAddress(c.Target.Address)
			if err == nil && ip != "" {
				c.Summary = "address " + ip
				outputs.Record("staticAddress", c.Target.Address.Name, "publicIp", ip)
			}
			return err
		}
	case TargetZone, TargetDns:
		dns, ok := provider.(DNSProvider)
		if !ok {
			return lacks("DNS")
		}
		if c.Target.Kind == TargetZone {
			return dns.EnsureZone(c.Target.Zone)
		}
		if c.Action == ActionDelete {
			return dns.DeleteDNS(c.Target.Dns)
		}
		record := c.Target.Dns
		var pending []Reference
		record.Values, pending = outputs.Resolve(record.Values)
		if len(pending) > 0 {
			return fmt.Errorf("reference %s is not resolved; apply its target first", pending[0])
		}
		return dns.PutDNS(record)
	case TargetGroup, TargetApp:
		identity, ok := provider.(IdentityProvider)
		if !ok {
			return lacks("identity")
		}
		switch {
		case c.Target.Kind == TargetGroup && c.Action == ActionDelete:
			return identity.DeleteGroup(c.Target.Group.Name)
		case c.Target.Kind == TargetGroup:
			return identity.PutGroup(c.Target.Group)
		case c.Action == ActionDelete:
			return identity.DeleteApp(c.Target.App.Tenant, c.Target.App.Name)
		default:
			return identity.PutApp(c.Target.App)
		}
	case TargetAppSecret:
		identity, ok := provider.(IdentityProvider)
		if !ok {
			return lacks("identity")
		}
		if c.Action != ActionDelete {
			value, err := identity.AddAppSecret(c.Target.App, c.Target.Secret)
			if err != nil {
				return err
			}
			c.Summary = "value " + value + " (shown once)"
		}
		for _, keyID := range c.Target.Stale {
			if err := identity.RemoveAppSecret(c.Target.App, keyID); err != nil {
				return err
			}
		}
		return nil
	case TargetRoleDefinition, TargetRoleAssignment:
		roles, ok := provider.(RoleProvider)
		if !ok {
			return lacks("role")
		}
		if c.Target.Kind == TargetRoleDefinition {
			if c.Action == ActionDelete {
				return roles.DeleteRoleDefinition(c.Target.RoleDefinition.Name, subscription)
			}
			return roles.PutRoleDefinition(c.Target.RoleDefinition, subscription)
		}
		ra := c.Target.RoleAssignment
		if c.Action == ActionDelete {
			return roles.DeleteRoleAssignment(LiveAssignment{ID: ra.ScopeID, PrincipalID: ra.PrincipalID, RoleID: ra.RoleID})
		}
		return roles.PutRoleAssignment(ra.PrincipalID, ra.RoleID, ra.ScopeID)
	case TargetResourceGroup:
		rgs, ok := provider.(ResourceGroupProvider)
		if !ok {
			return lacks("resource-group")
		}
		if c.Action == ActionDelete {
			return rgs.DeleteResourceGroup(c.Target.ResourceGroup.Name)
		}
		return rgs.PutResourceGroup(c.Target.ResourceGroup)
	}
	return nil
}

// planHost plans the host kinds in dependency order, iamRole, bucket,
// firewall, instance, staticAddress, and appends their prune deletes in
// reverse order so an address goes before its instance and a role last.
func planHost(provider LiveProvider, bundle *Bundle, options *Options, changes *[]Change) error {
	var deletes [][]Change
	collect := func(kindDeletes []Change) { deletes = append(deletes, kindDeletes) }

	if wanted(options, "iamRole") && len(bundle.IamRoles) > 0 {
		roles, ok := provider.(IamRoleProvider)
		if !ok {
			return unsupportedKind(options.Provider, "iamRole")
		}
		live, err := roles.ListIamRoles()
		if err != nil {
			return err
		}
		desired := map[string]bool{}
		for _, item := range bundle.IamRoles {
			desired[item.Name] = true
			current, found := findIamRole(live, item.Name)
			switch {
			case !found:
				*changes = append(*changes, change("iamRole", ActionCreate, "iamRole|"+item.Name, "missing", Target{Kind: TargetIamRole, IamRole: item}))
			case !stringsEqual(normalized(current.ManagedPolicies), normalized(item.ManagedPolicies)) || !samePolicies(current.InlinePolicies, item.InlinePolicies) || current.InstanceProfile != item.InstanceProfile:
				c := change("iamRole", ActionUpdate, "iamRole|"+item.Name, "policies or instance profile differ", Target{Kind: TargetIamRole, IamRole: item})
				c.Diffs = iamRoleDiffs(current, item)
				*changes = append(*changes, c)
			}
		}
		var kindDeletes []Change
		if options.PruneHosts {
			for _, item := range live {
				if !desired[item.Name] {
					kindDeletes = append(kindDeletes, change("iamRole", ActionDelete, "iamRole|"+item.Name, "absent from specs", Target{Kind: TargetIamRole, IamRole: item}))
				}
			}
		}
		collect(kindDeletes)
	}

	if wanted(options, "bucket") && len(bundle.Buckets) > 0 {
		storage, ok := provider.(StorageProvider)
		if !ok {
			return unsupportedKind(options.Provider, "bucket")
		}
		live, err := storage.ListBuckets()
		if err != nil {
			return err
		}
		desired := map[string]bool{}
		for _, item := range bundle.Buckets {
			desired[item.Name] = true
			current, found := findBucket(live, item.Name)
			switch {
			case !found:
				*changes = append(*changes, change("bucket", ActionCreate, "bucket|"+item.Name, "missing", Target{Kind: TargetBucket, Bucket: item}))
			case !sameLocation(current.Region, item.Region):
				*changes = append(*changes, change("bucket", ActionReplace, "bucket|"+item.Name, "region is immutable", Target{Kind: TargetBucket, Bucket: item}))
			case current.PublicAccessBlock != item.PublicAccessBlock || current.Encryption != item.Encryption:
				c := change("bucket", ActionUpdate, "bucket|"+item.Name, "public access block or encryption differ", Target{Kind: TargetBucket, Bucket: item})
				c.Diffs = bucketDiffs(current, item)
				*changes = append(*changes, c)
			}
		}
		var kindDeletes []Change
		if options.PruneHosts {
			for _, item := range live {
				if !desired[item.Name] {
					kindDeletes = append(kindDeletes, change("bucket", ActionDelete, "bucket|"+item.Name, "absent from specs", Target{Kind: TargetBucket, Bucket: item}))
				}
			}
		}
		collect(kindDeletes)
	}

	if wanted(options, "firewall") && len(bundle.Firewalls) > 0 {
		network, ok := provider.(NetworkProvider)
		if !ok {
			return unsupportedKind(options.Provider, "firewall")
		}
		live, err := network.ListFirewalls()
		if err != nil {
			return err
		}
		desired := map[string]bool{}
		for _, item := range bundle.Firewalls {
			desired[item.Name] = true
			current, found := findFirewall(live, item.Name)
			switch {
			case !found:
				*changes = append(*changes, change("firewall", ActionCreate, "firewall|"+item.Name, "missing", Target{Kind: TargetFirewall, Firewall: item}))
			case current.VPC != item.VPC:
				*changes = append(*changes, change("firewall", ActionReplace, "firewall|"+item.Name, "vpc is immutable", Target{Kind: TargetFirewall, Firewall: item}))
			case !sameRules(current.Rules, item.Rules):
				updated := item
				updated.ID = current.ID
				c := change("firewall", ActionUpdate, "firewall|"+item.Name, "rules differ", Target{Kind: TargetFirewall, Firewall: updated})
				c.Diffs = firewallDiffs(current, item)
				*changes = append(*changes, c)
			}
		}
		var kindDeletes []Change
		if options.PruneHosts {
			for _, item := range live {
				if !desired[item.Name] {
					kindDeletes = append(kindDeletes, change("firewall", ActionDelete, "firewall|"+item.Name, "absent from specs", Target{Kind: TargetFirewall, Firewall: item}))
				}
			}
		}
		collect(kindDeletes)
	}

	if wanted(options, "instance") && len(bundle.Instances) > 0 {
		compute, ok := provider.(ComputeProvider)
		if !ok {
			return unsupportedKind(options.Provider, "instance")
		}
		live, err := compute.ListInstances()
		if err != nil {
			return err
		}
		recordInstanceOutputs(options, live)
		desired := map[string]bool{}
		for _, item := range bundle.Instances {
			desired[item.Name] = true
			resolved := item
			image, err := compute.ResolveImage(item.Image)
			if err != nil {
				return err
			}
			resolved.Image = image
			current, found := findInstance(live, item.Name)
			if !found {
				*changes = append(*changes, change("instance", ActionCreate, "instance|"+item.Name, "missing", Target{Kind: TargetInstance, Instance: resolved}))
				continue
			}
			var immutable []string
			if current.Type != resolved.Type {
				immutable = append(immutable, "type")
			}
			if current.Image != resolved.Image {
				immutable = append(immutable, "image")
			}
			if current.RootDisk.Type != resolved.RootDisk.Type {
				immutable = append(immutable, "root disk type")
			}
			if current.RootDisk.Encrypted != resolved.RootDisk.Encrypted {
				immutable = append(immutable, "root disk encryption")
			}
			if len(immutable) > 0 {
				*changes = append(*changes, change("instance", ActionReplace, "instance|"+item.Name, strings.Join(immutable, ", ")+" is immutable", Target{Kind: TargetInstance, Instance: resolved}))
				continue
			}
			if current.Role != resolved.Role || current.Firewall != resolved.Firewall || current.Imdsv2Required != resolved.Imdsv2Required ||
				current.RootDisk.Size != resolved.RootDisk.Size || !tagsSatisfy(resolved.Tags, current.Tags) {
				updated := resolved
				updated.ID = current.ID
				merged := map[string]string{}
				maps.Copy(merged, current.Tags)
				maps.Copy(merged, resolved.Tags)
				updated.Tags = merged
				c := change("instance", ActionUpdate, "instance|"+item.Name, "role, firewall, metadata, disk size, or declared tags differ", Target{Kind: TargetInstance, Instance: updated})
				c.Diffs = instanceDiffs(current, resolved)
				*changes = append(*changes, c)
			}
		}
		var kindDeletes []Change
		if options.PruneHosts {
			for _, item := range live {
				if !desired[item.Name] {
					kindDeletes = append(kindDeletes, change("instance", ActionDelete, "instance|"+item.Name, "absent from specs", Target{Kind: TargetInstance, Instance: item}))
				}
			}
		}
		collect(kindDeletes)
	}

	if wanted(options, "staticAddress") && len(bundle.Addresses) > 0 {
		compute, ok := provider.(ComputeProvider)
		if !ok {
			return unsupportedKind(options.Provider, "staticAddress")
		}
		live, err := compute.ListAddresses()
		if err != nil {
			return err
		}
		recordAddressOutputs(options, live)
		desired := map[string]bool{}
		for _, item := range bundle.Addresses {
			desired[item.Name] = true
			current, found := findAddress(live, item.Name)
			switch {
			case !found:
				*changes = append(*changes, change("staticAddress", ActionCreate, "staticAddress|"+item.Name, "missing", Target{Kind: TargetAddress, Address: item}))
			case current.Instance != item.Instance:
				updated := item
				updated.ID = current.ID
				c := change("staticAddress", ActionUpdate, "staticAddress|"+item.Name, "attached instance differs", Target{Kind: TargetAddress, Address: updated})
				c.Diffs = []FieldDiff{{Field: "instance", Old: current.Instance, New: item.Instance}}
				*changes = append(*changes, c)
			}
		}
		var kindDeletes []Change
		if options.PruneHosts {
			for _, item := range live {
				if !desired[item.Name] {
					kindDeletes = append(kindDeletes, change("staticAddress", ActionDelete, "staticAddress|"+item.Name, "absent from specs", Target{Kind: TargetAddress, Address: item}))
				}
			}
		}
		collect(kindDeletes)
	}

	for i := len(deletes) - 1; i >= 0; i-- {
		*changes = append(*changes, deletes[i]...)
	}
	return nil
}

func findIamRole(items []IamRole, name string) (IamRole, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return IamRole{}, false
}

func findBucket(items []Bucket, name string) (Bucket, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return Bucket{}, false
}

func findFirewall(items []Firewall, name string) (Firewall, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return Firewall{}, false
}

func findInstance(items []Instance, name string) (Instance, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return Instance{}, false
}

func findAddress(items []StaticAddress, name string) (StaticAddress, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return StaticAddress{}, false
}

func iamRoleDiffs(current, desired IamRole) []FieldDiff {
	diffs := setDiffs("managedPolicies", current.ManagedPolicies, desired.ManagedPolicies)
	var names []string
	for name := range desired.InlinePolicies {
		names = append(names, name)
	}
	for name := range current.InlinePolicies {
		if _, ok := desired.InlinePolicies[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		before, hadBefore := current.InlinePolicies[name]
		after, hasAfter := desired.InlinePolicies[name]
		switch {
		case !hadBefore:
			diffs = append(diffs, FieldDiff{Field: "inlinePolicy " + name, New: "added"})
		case !hasAfter:
			diffs = append(diffs, FieldDiff{Field: "inlinePolicy " + name, Old: "removed"})
		case canonicalJSON(before) != canonicalJSON(after):
			diffs = append(diffs, FieldDiff{Field: "inlinePolicy " + name, Old: "document", New: "changed"})
		}
	}
	if current.InstanceProfile != desired.InstanceProfile {
		diffs = append(diffs, FieldDiff{Field: "instanceProfile", Old: strconv.FormatBool(current.InstanceProfile), New: strconv.FormatBool(desired.InstanceProfile)})
	}
	return diffs
}

func bucketDiffs(current, desired Bucket) []FieldDiff {
	var diffs []FieldDiff
	if current.PublicAccessBlock != desired.PublicAccessBlock {
		diffs = append(diffs, FieldDiff{Field: "publicAccessBlock", Old: fmt.Sprintf("%+v", current.PublicAccessBlock), New: fmt.Sprintf("%+v", desired.PublicAccessBlock)})
	}
	if current.Encryption != desired.Encryption {
		diffs = append(diffs, FieldDiff{Field: "encryption", Old: current.Encryption, New: desired.Encryption})
	}
	return diffs
}

func firewallDiffs(current, desired Firewall) []FieldDiff {
	return setDiffs("rule", ruleKeys(current.Rules), ruleKeys(desired.Rules))
}

func instanceDiffs(current, desired Instance) []FieldDiff {
	var diffs []FieldDiff
	scalar := func(field, before, after string) {
		if before != after {
			diffs = append(diffs, FieldDiff{Field: field, Old: before, New: after})
		}
	}
	scalar("role", current.Role, desired.Role)
	scalar("firewall", current.Firewall, desired.Firewall)
	scalar("imdsv2Required", strconv.FormatBool(current.Imdsv2Required), strconv.FormatBool(desired.Imdsv2Required))
	scalar("rootDisk.size", strconv.FormatInt(current.RootDisk.Size, 10), strconv.FormatInt(desired.RootDisk.Size, 10))
	var keys []string
	for k := range desired.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if current.Tags[k] != desired.Tags[k] {
			diffs = append(diffs, FieldDiff{Field: "tag " + k, Old: current.Tags[k], New: desired.Tags[k]})
		}
	}
	return diffs
}

func recordInstanceOutputs(options *Options, live []Instance) {
	outputs := options.outputs()
	for _, item := range live {
		outputs.Record("instance", item.Name, "id", item.ID)
	}
	outputs.listed["instance"] = true
}

func recordAddressOutputs(options *Options, live []StaticAddress) {
	outputs := options.outputs()
	for _, item := range live {
		outputs.Record("staticAddress", item.Name, "publicIp", item.PublicIP)
	}
	outputs.listed["staticAddress"] = true
}

// recordReferencedOutputs lists the kinds the bundle's records reference
// when planning has not already listed them, such as under -k
// dnsRecordSet, so references resolve from live state regardless of the
// kind filter.
func recordReferencedOutputs(provider LiveProvider, bundle *Bundle, options *Options) error {
	outputs := options.outputs()
	for kind := range bundle.referencedKinds() {
		if outputs.listed[kind] {
			continue
		}
		compute, ok := provider.(ComputeProvider)
		if !ok {
			return unsupportedKind(options.Provider, kind)
		}
		switch kind {
		case "instance":
			live, err := compute.ListInstances()
			if err != nil {
				return err
			}
			recordInstanceOutputs(options, live)
		case "staticAddress":
			live, err := compute.ListAddresses()
			if err != nil {
				return err
			}
			recordAddressOutputs(options, live)
		}
	}
	return nil
}

// planBudgets plans budget creates and updates, returning the prune deletes
// so Plan can append them after the host deletes.
func planBudgets(provider LiveProvider, bundle *Bundle, options *Options, changes *[]Change) ([]Change, error) {
	if !wanted(options, "budget") || len(bundle.Budgets) == 0 {
		return nil, nil
	}
	monitoring, ok := provider.(MonitoringProvider)
	if !ok {
		return nil, unsupportedKind(options.Provider, "budget")
	}
	live, err := monitoring.ListBudgets()
	if err != nil {
		return nil, err
	}
	desired := map[string]bool{}
	for _, item := range bundle.Budgets {
		desired[item.Name] = true
		current, found := findBudget(live, item.Name)
		switch {
		case !found:
			*changes = append(*changes, change("budget", ActionCreate, "budget|"+item.Name, "missing", Target{Kind: TargetBudget, Budget: item}))
		case !sameAmount(current.Amount, item.Amount) || current.Currency != item.Currency || !stringsEqual(alertKeys(current.Alerts), alertKeys(item.Alerts)):
			c := change("budget", ActionUpdate, "budget|"+item.Name, "amount or alerts differ", Target{Kind: TargetBudget, Budget: item})
			c.Diffs = budgetDiffs(current, item)
			*changes = append(*changes, c)
		}
	}
	var deletes []Change
	if options.PruneMonitoring {
		for _, item := range live {
			if !desired[item.Name] {
				deletes = append(deletes, change("budget", ActionDelete, "budget|"+item.Name, "absent from specs", Target{Kind: TargetBudget, Budget: item}))
			}
		}
	}
	return deletes, nil
}

// planHealthChecks plans health check creates, updates, replaces, and prune
// deletes, resolving each target through the run's outputs.
func planHealthChecks(provider LiveProvider, bundle *Bundle, options *Options, changes *[]Change) error {
	if !wanted(options, "healthCheck") || len(bundle.HealthChecks) == 0 {
		return nil
	}
	monitoring, ok := provider.(MonitoringProvider)
	if !ok {
		return unsupportedKind(options.Provider, "healthCheck")
	}
	live, err := monitoring.ListHealthChecks()
	if err != nil {
		return err
	}
	desired := map[string]bool{}
	for _, item := range bundle.HealthChecks {
		desired[item.Name] = true
		resolved := item
		targets, pending := options.outputs().Resolve([]string{item.Target})
		resolved.Target = targets[0]
		current, found := findHealthCheck(live, item.Name)
		switch {
		case !found && len(pending) > 0:
			*changes = append(*changes, change("healthCheck", ActionCreate, "healthCheck|"+item.Name, pendingSummary(pending), Target{Kind: TargetHealthCheck, HealthCheck: item}))
		case !found:
			*changes = append(*changes, change("healthCheck", ActionCreate, "healthCheck|"+item.Name, "missing", Target{Kind: TargetHealthCheck, HealthCheck: resolved}))
		case current.Protocol != item.Protocol || current.Interval != item.Interval:
			var immutable []string
			if current.Protocol != item.Protocol {
				immutable = append(immutable, "protocol")
			}
			if current.Interval != item.Interval {
				immutable = append(immutable, "interval")
			}
			*changes = append(*changes, change("healthCheck", ActionReplace, "healthCheck|"+item.Name, strings.Join(immutable, ", ")+" is immutable", Target{Kind: TargetHealthCheck, HealthCheck: resolved}))
		case len(pending) > 0:
			updated := item
			updated.ID = current.ID
			c := change("healthCheck", ActionUpdate, "healthCheck|"+item.Name, pendingSummary(pending), Target{Kind: TargetHealthCheck, HealthCheck: updated})
			c.Diffs = healthCheckDiffs(current, resolved)
			*changes = append(*changes, c)
		case current.Target != resolved.Target || current.Port != item.Port || current.Path != item.Path || current.Host != item.Host ||
			current.FailureThreshold != item.FailureThreshold || !stringsEqual(normalized(current.Emails), normalized(item.Emails)) || !current.Alarm:
			updated := resolved
			updated.ID = current.ID
			c := change("healthCheck", ActionUpdate, "healthCheck|"+item.Name, "target, probe, emails, or alarm differ", Target{Kind: TargetHealthCheck, HealthCheck: updated})
			c.Diffs = healthCheckDiffs(current, resolved)
			*changes = append(*changes, c)
		}
	}
	if options.PruneMonitoring {
		for _, item := range live {
			if !desired[item.Name] {
				*changes = append(*changes, change("healthCheck", ActionDelete, "healthCheck|"+item.Name, "absent from specs", Target{Kind: TargetHealthCheck, HealthCheck: item}))
			}
		}
	}
	return nil
}

func findBudget(items []Budget, name string) (Budget, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return Budget{}, false
}

func findHealthCheck(items []HealthCheck, name string) (HealthCheck, bool) {
	for _, item := range items {
		if item.Name == name {
			return item, true
		}
	}
	return HealthCheck{}, false
}

func budgetDiffs(current, desired Budget) []FieldDiff {
	var diffs []FieldDiff
	if !sameAmount(current.Amount, desired.Amount) {
		diffs = append(diffs, FieldDiff{Field: "amount", Old: current.Amount, New: desired.Amount})
	}
	if current.Currency != desired.Currency {
		diffs = append(diffs, FieldDiff{Field: "currency", Old: current.Currency, New: desired.Currency})
	}
	return append(diffs, setDiffs("alert", alertKeys(current.Alerts), alertKeys(desired.Alerts))...)
}

func healthCheckDiffs(current, desired HealthCheck) []FieldDiff {
	var diffs []FieldDiff
	scalar := func(field, before, after string) {
		if before != after {
			diffs = append(diffs, FieldDiff{Field: field, Old: before, New: after})
		}
	}
	scalar("target", current.Target, desired.Target)
	scalar("port", strconv.FormatInt(current.Port, 10), strconv.FormatInt(desired.Port, 10))
	scalar("path", current.Path, desired.Path)
	scalar("host", current.Host, desired.Host)
	scalar("failureThreshold", strconv.FormatInt(current.FailureThreshold, 10), strconv.FormatInt(desired.FailureThreshold, 10))
	if !current.Alarm && desired.Alarm {
		diffs = append(diffs, FieldDiff{Field: "alarm", Old: "missing", New: "present"})
	}
	return append(diffs, setDiffs("email", current.Emails, desired.Emails)...)
}

// now is the clock secret expiry is judged against; tests replace it.
var now = time.Now

// planApp plans one app registration against its live state: a create
// when it is missing, one update when any managed field differs, and one
// change per secret that is missing, stale, or (under identity pruning)
// unwanted. An unset sign-in field or secrets list is never compared.
func planApp(current *AppRegistration, item AppRegistration, options *Options) []Change {
	var changes []Change
	key := item.Key()
	if current == nil {
		changes = append(changes, change("appRegistration", ActionCreate, key, "missing", Target{Kind: TargetApp, App: item}))
		for _, secret := range item.Secrets {
			changes = append(changes, change("appRegistration", ActionCreate, secretKey(key, secret.Name), "missing", Target{Kind: TargetAppSecret, App: item, Secret: secret}))
		}
		return changes
	}
	var diffs []FieldDiff
	diffs = append(diffs, setDiffs("owners", current.Owners, item.Owners)...)
	if current.ServicePrincipal != item.ServicePrincipal {
		diffs = append(diffs, FieldDiff{Field: "servicePrincipal", Old: fmt.Sprintf("%t", current.ServicePrincipal), New: fmt.Sprintf("%t", item.ServicePrincipal)})
	}
	summary := "owners or service principal differ"
	signIn := 0
	if item.SignInAudience != "" && item.SignInAudience != current.SignInAudience {
		diffs = append(diffs, FieldDiff{Field: "signInAudience", Old: current.SignInAudience, New: item.SignInAudience})
		signIn++
	}
	if item.RedirectURIs != nil && !stringsEqual(sortedCopy(current.RedirectURIs), sortedCopy(item.RedirectURIs)) {
		diffs = append(diffs, setDiffs("redirectUris", current.RedirectURIs, item.RedirectURIs)...)
		signIn++
	}
	if item.IDTokenIssuance != nil && (current.IDTokenIssuance == nil || *current.IDTokenIssuance != *item.IDTokenIssuance) {
		old := "false"
		if current.IDTokenIssuance != nil {
			old = fmt.Sprintf("%t", *current.IDTokenIssuance)
		}
		diffs = append(diffs, FieldDiff{Field: "idTokenIssuance", Old: old, New: fmt.Sprintf("%t", *item.IDTokenIssuance)})
		signIn++
	}
	if signIn > 0 {
		summary = "sign-in settings differ"
	}
	if len(diffs) > 0 {
		c := change("appRegistration", ActionUpdate, key, summary, Target{Kind: TargetApp, App: item})
		c.Diffs = diffs
		changes = append(changes, c)
	}
	if item.Secrets == nil {
		return changes
	}
	wanted := map[string]bool{}
	for _, secret := range item.Secrets {
		wanted[secret.Name] = true
		var live []Secret
		for _, l := range current.Secrets {
			if l.Name == secret.Name {
				live = append(live, l)
			}
		}
		if len(live) == 0 {
			changes = append(changes, change("appRegistration", ActionCreate, secretKey(key, secret.Name), "missing", Target{Kind: TargetAppSecret, App: item, Secret: secret}))
			continue
		}
		reason := staleReason(live, secret)
		if reason == "" {
			continue
		}
		var stale []string
		for _, l := range live {
			stale = append(stale, l.KeyID)
		}
		c := change("appRegistration", ActionUpdate, secretKey(key, secret.Name), "rotate: "+reason, Target{Kind: TargetAppSecret, App: item, Secret: secret, Stale: stale})
		c.Diffs = secretDiffs(live, secret)
		changes = append(changes, c)
	}
	if options.PruneIdentities {
		for _, l := range current.Secrets {
			if !wanted[l.Name] {
				changes = append(changes, change("appRegistration", ActionDelete, secretKey(key, l.Name), "absent from specs", Target{Kind: TargetAppSecret, App: item, Secret: l, Stale: []string{l.KeyID}}))
			}
		}
	}
	return changes
}

// secretKey names one secret change under its registration's key.
func secretKey(appKey, secret string) string {
	return appKey + " secret " + secret
}

// staleReason says why a named secret's live credentials must be rotated:
// every one has expired, or one was issued before the spec's issuedAfter.
// The newest credential speaks for the name, so an older leftover never
// forces a rotation on its own.
func staleReason(live []Secret, secret Secret) string {
	newest := live[0]
	for _, l := range live[1:] {
		if l.Expires.After(newest.Expires) {
			newest = l
		}
	}
	switch {
	case !newest.Expires.IsZero() && !newest.Expires.After(now()):
		return "expired"
	case !secret.IssuedAfter.IsZero() && newest.Issued.Before(secret.IssuedAfter):
		return "issued before " + secret.IssuedAfter.UTC().Format(time.RFC3339)
	}
	return ""
}

// secretDiffs renders the live credential dates behind a rotation; the
// value itself is never known to plan.
func secretDiffs(live []Secret, secret Secret) []FieldDiff {
	var diffs []FieldDiff
	for _, l := range live {
		diffs = append(diffs, FieldDiff{Field: "issued", Old: l.Issued.UTC().Format(time.RFC3339), New: "now"})
		diffs = append(diffs, FieldDiff{Field: "expires", Old: l.Expires.UTC().Format(time.RFC3339), New: fmt.Sprintf("%d days from now", secret.ValidForDays)})
	}
	return diffs
}
