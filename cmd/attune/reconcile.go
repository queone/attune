package main

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

// Action is the reconciliation action for one Change.
type Action int

const (
	ActionCreate Action = iota
	ActionUpdate
	ActionDelete
)

func (a Action) String() string {
	switch a {
	case ActionCreate:
		return "create"
	case ActionUpdate:
		return "update"
	case ActionDelete:
		return "delete"
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
	TargetRoleDefinition
	TargetRoleAssignment
	TargetResourceGroup
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
	RoleDefinition RoleDefinition
	RoleAssignment RoleAssignmentTarget
	ResourceGroup  ResourceGroup
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

// Options controls which kinds Plan considers and which prune policies apply.
type Options struct {
	Subscription        string
	Kind                string
	PruneDNS            bool
	PruneIdentities     bool
	PruneRoles          bool
	PruneResourceGroups bool
}

// LiveAssignment is a role assignment as read from the live provider.
type LiveAssignment struct {
	ID          string
	PrincipalID string
	RoleID      string
	Scope       string
}

// Provider is the live-state interface Plan/Apply reconcile against.
type Provider interface {
	EnsureZone(zone string) error
	HasZone(zone string) (bool, error)
	ListDNS(zone string) ([]DnsRecord, error)
	PutDNS(record DnsRecord) error
	DeleteDNS(record DnsRecord) error
	GetGroup(name string) (*SecurityGroup, error)
	ListGroupNames() ([]string, error)
	PutGroup(group SecurityGroup) error
	DeleteGroup(name string) error
	GetApp(name string) (*AppRegistration, error)
	ListAppNames() ([]string, error)
	PutApp(app AppRegistration) error
	DeleteApp(name string) error
	ListRoleDefinitions(subscription string) ([]RoleDefinition, error)
	PutRoleDefinition(role RoleDefinition, subscription string) error
	DeleteRoleDefinition(name, subscription string) error
	ResolvePrincipal(name, principalType string) (string, error)
	ResolveRole(name, subscription string) (string, error)
	ListRoleAssignments(scope string) ([]LiveAssignment, error)
	PutRoleAssignment(principal, role, scope string) error
	DeleteRoleAssignment(assignment LiveAssignment) error
	ListResourceGroups() ([]ResourceGroup, error)
	PutResourceGroup(group ResourceGroup) error
	DeleteResourceGroup(name string) error
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
// appRegistration, roleDefinition, roleAssignment, dnsRecordSet.
func Plan(provider Provider, bundle *Bundle, options *Options) ([]Change, error) {
	var changes []Change

	if wanted(options, "resourceGroup") && len(bundle.ResourceGroups) > 0 {
		live, err := provider.ListResourceGroups()
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
		for _, item := range bundle.Groups {
			current, err := provider.GetGroup(item.Name)
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
			names, err := provider.ListGroupNames()
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
		for _, item := range bundle.Apps {
			current, err := provider.GetApp(item.Name)
			if err != nil {
				return nil, err
			}
			switch {
			case current == nil:
				changes = append(changes, change("appRegistration", ActionCreate, "appRegistration|"+item.Name, "missing", Target{Kind: TargetApp, App: item}))
			case !stringsEqual(sortedCopy(current.Owners), sortedCopy(item.Owners)) || current.ServicePrincipal != item.ServicePrincipal:
				c := change("appRegistration", ActionUpdate, "appRegistration|"+item.Name, "owners or service principal differ", Target{Kind: TargetApp, App: item})
				c.Diffs = setDiffs("owners", current.Owners, item.Owners)
				if current.ServicePrincipal != item.ServicePrincipal {
					c.Diffs = append(c.Diffs, FieldDiff{Field: "servicePrincipal", Old: fmt.Sprintf("%t", current.ServicePrincipal), New: fmt.Sprintf("%t", item.ServicePrincipal)})
				}
				changes = append(changes, c)
			}
		}
		if options.PruneIdentities {
			desired := map[string]bool{}
			for _, item := range bundle.Apps {
				desired[item.Name] = true
			}
			names, err := provider.ListAppNames()
			if err != nil {
				return nil, err
			}
			for _, name := range names {
				if !desired[name] {
					changes = append(changes, change("appRegistration", ActionDelete, "appRegistration|"+name, "absent from specs", Target{Kind: TargetApp, App: AppRegistration{Name: name}}))
				}
			}
		}
	}

	if wanted(options, "roleDefinition") && len(bundle.RoleDefinitions) > 0 {
		live, err := provider.ListRoleDefinitions(options.Subscription)
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
		if err := planRoleAssignments(provider, bundle, options, &changes); err != nil {
			return nil, err
		}
	}

	if wanted(options, "dnsRecordSet") && len(bundle.Dns) > 0 {
		if err := planDNS(provider, bundle, options, &changes); err != nil {
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

func planRoleAssignments(provider Provider, bundle *Bundle, options *Options, changes *[]Change) error {
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

func planDNS(provider Provider, bundle *Bundle, options *Options, changes *[]Change) error {
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
			current, found := findDNS(live, item.Key())
			switch {
			case !found:
				*changes = append(*changes, change("dnsRecordSet", ActionCreate, item.Key(), "missing", Target{Kind: TargetDns, Dns: item}))
			case !current.SameData(item):
				c := change("dnsRecordSet", ActionUpdate, item.Key(), "TTL or values differ", Target{Kind: TargetDns, Dns: item})
				c.Diffs = dnsRecordDiffs(current, item)
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
func Apply(provider Provider, changes []Change, subscription string, report func(Change)) error {
	for _, c := range changes {
		var err error
		switch c.Target.Kind {
		case TargetZone:
			err = provider.EnsureZone(c.Target.Zone)
		case TargetDns:
			if c.Action == ActionDelete {
				err = provider.DeleteDNS(c.Target.Dns)
			} else {
				err = provider.PutDNS(c.Target.Dns)
			}
		case TargetGroup:
			if c.Action == ActionDelete {
				err = provider.DeleteGroup(c.Target.Group.Name)
			} else {
				err = provider.PutGroup(c.Target.Group)
			}
		case TargetApp:
			if c.Action == ActionDelete {
				err = provider.DeleteApp(c.Target.App.Name)
			} else {
				err = provider.PutApp(c.Target.App)
			}
		case TargetRoleDefinition:
			if c.Action == ActionDelete {
				err = provider.DeleteRoleDefinition(c.Target.RoleDefinition.Name, subscription)
			} else {
				err = provider.PutRoleDefinition(c.Target.RoleDefinition, subscription)
			}
		case TargetRoleAssignment:
			ra := c.Target.RoleAssignment
			if c.Action == ActionDelete {
				err = provider.DeleteRoleAssignment(LiveAssignment{ID: ra.ScopeID, PrincipalID: ra.PrincipalID, RoleID: ra.RoleID})
			} else {
				err = provider.PutRoleAssignment(ra.PrincipalID, ra.RoleID, ra.ScopeID)
			}
		case TargetResourceGroup:
			if c.Action == ActionDelete {
				err = provider.DeleteResourceGroup(c.Target.ResourceGroup.Name)
			} else {
				err = provider.PutResourceGroup(c.Target.ResourceGroup)
			}
		}
		if err != nil {
			return err
		}
		if report != nil {
			report(c)
		}
	}
	return nil
}
