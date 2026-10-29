package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
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

// AppRegistration is an application registration. Tenant names the
// directory it lives in; empty means the signed-in tenant. SignInAudience,
// RedirectURIs, IDTokenIssuance, and Secrets are unmanaged when unset
// (empty, nil, nil, nil): neither sent nor compared.
type AppRegistration struct {
	Provider         string
	Name             string
	Owners           []string
	ServicePrincipal bool
	Tenant           string
	SignInAudience   string
	RedirectURIs     []string
	IDTokenIssuance  *bool
	Secrets          []Secret
}

// Tenants lists, sorted, every tenant an app registration names other
// than the signed-in one.
func (b *Bundle) Tenants() []string {
	var tenants []string
	for _, app := range b.Apps {
		if app.Tenant != "" && !slices.Contains(tenants, app.Tenant) {
			tenants = append(tenants, app.Tenant)
		}
	}
	sort.Strings(tenants)
	return tenants
}

// Key is the change key: the name, with @tenant appended outside the
// signed-in tenant.
func (a AppRegistration) Key() string {
	if a.Tenant == "" {
		return "appRegistration|" + a.Name
	}
	return "appRegistration|" + a.Name + "@" + a.Tenant
}

// Secret is one client secret of an app registration, identified by its
// display name. ValidForDays and IssuedAfter come from the spec; KeyID,
// Issued, and Expires describe the live credential.
type Secret struct {
	Name         string
	ValidForDays int
	IssuedAfter  time.Time
	DeliverTo    *Delivery
	KeyID        string
	Issued       time.Time
	Expires      time.Time
}

// Delivery names where apply writes a secret's value instead of printing
// it: a provider, the target field that provider accepts, and the target.
type Delivery struct {
	Provider string
	Field    string
	Target   string
}

// String renders a delivery for plan and apply lines, such as
// "aws parameter /app/secret".
func (d Delivery) String() string {
	return d.Provider + " " + d.Field + " " + d.Target
}

// DeliveryProviders lists, sorted, every provider a secret delivers to.
func (b *Bundle) DeliveryProviders() []string {
	var names []string
	for _, app := range b.Apps {
		for _, secret := range app.Secrets {
			if secret.DeliverTo != nil && !slices.Contains(names, secret.DeliverTo.Provider) {
				names = append(names, secret.DeliverTo.Provider)
			}
		}
	}
	sort.Strings(names)
	return names
}

// signInAudiences is every value Graph accepts for signInAudience.
var signInAudiences = []string{"AzureADMyOrg", "AzureADMultipleOrgs", "AzureADandPersonalMicrosoftAccount", "PersonalMicrosoftAccount"}

// secretMaxDays is the longest life a secret may be given, the portal's
// two-year maximum and Graph's default.
const secretMaxDays = 730

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
	Budgets         []Budget
	HealthChecks    []HealthCheck
}

// Len returns the total number of specs across every kind.
func (b Bundle) Len() int {
	return len(b.Dns) + len(b.Groups) + len(b.Apps) + len(b.RoleDefinitions) + len(b.RoleAssignments) + len(b.ResourceGroups) +
		len(b.IamRoles) + len(b.Buckets) + len(b.Firewalls) + len(b.Instances) + len(b.Addresses) + len(b.Budgets) + len(b.HealthChecks)
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
	for _, item := range b.Budgets {
		seen[item.Provider] = true
	}
	for _, item := range b.HealthChecks {
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
	for _, item := range b.Budgets {
		if item.Provider == provider {
			part.Budgets = append(part.Budgets, item)
		}
	}
	for _, item := range b.HealthChecks {
		if item.Provider == provider {
			part.HealthChecks = append(part.HealthChecks, item)
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
// parameter path the provider resolves. MetadataHopLimit of zero in a spec
// leaves the live hop limit unmanaged. Tags use merge semantics. ID is the
// live identifier, empty in a spec.
type Instance struct {
	Provider         string
	ID               string
	Name             string
	Type             string
	Image            string
	Role             string
	Firewall         string
	Imdsv2Required   bool
	MetadataHopLimit int
	RootDisk         RootDisk
	Tags             map[string]string
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

// Reference names another spec's live output inside a value, written as
// `${kind:name:field}`. A value is either plain text or exactly one
// reference.
type Reference struct {
	Kind  string
	Name  string
	Field string
}

func (r Reference) String() string { return "${" + r.Kind + ":" + r.Name + ":" + r.Field + "}" }

// referenceOutputs lists the fields attune can read from each kind's live
// state, so a reference can be checked offline.
var referenceOutputs = map[string][]string{
	"staticAddress": {"publicIp"},
	"instance":      {"id"},
}

// parseReference reads a value. It returns ok=false for plain text, the
// reference for a well-formed `${kind:name:field}`, and an error for text
// that opens `${` without being one.
func parseReference(value string) (Reference, bool, error) {
	if !strings.Contains(value, "${") {
		return Reference{}, false, nil
	}
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") || strings.Count(value, "${") != 1 {
		return Reference{}, false, fmt.Errorf("value %q is not a reference of the form ${kind:name:field}", value)
	}
	parts := strings.Split(value[2:len(value)-1], ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Reference{}, false, fmt.Errorf("value %q is not a reference of the form ${kind:name:field}", value)
	}
	return Reference{Kind: parts[0], Name: parts[1], Field: parts[2]}, true, nil
}

// Outputs holds the live outputs a run has learned, keyed by reference, and
// which kinds it has already listed to learn them.
type Outputs struct {
	values map[string]string
	listed map[string]bool
}

// NewOutputs returns an empty output set.
func NewOutputs() *Outputs {
	return &Outputs{values: map[string]string{}, listed: map[string]bool{}}
}

// Record stores one output when value is non-empty.
func (o *Outputs) Record(kind, name, field, value string) {
	if value != "" {
		o.values[Reference{Kind: kind, Name: name, Field: field}.String()] = value
	}
}

// Resolve substitutes every reference in values that has a recorded output
// and returns the references that do not, leaving those values as written.
func (o *Outputs) Resolve(values []string) ([]string, []Reference) {
	resolved := make([]string, 0, len(values))
	var pending []Reference
	for _, value := range values {
		ref, ok, err := parseReference(value)
		if err != nil || !ok {
			resolved = append(resolved, value)
			continue
		}
		if output, found := o.values[ref.String()]; found {
			resolved = append(resolved, output)
		} else {
			resolved = append(resolved, value)
			pending = append(pending, ref)
		}
	}
	return resolved, pending
}

// referencedKinds returns the kinds the bundle's referencing fields name:
// record values and health check targets.
func (b Bundle) referencedKinds() map[string]bool {
	kinds := map[string]bool{}
	for _, item := range b.referencingValues() {
		if ref, ok, err := parseReference(item.value); err == nil && ok {
			kinds[ref.Kind] = true
		}
	}
	return kinds
}

// referencingValue is one field that may hold a reference, with the key
// and provider of the spec it belongs to.
type referencingValue struct {
	key      string
	provider string
	value    string
}

// referencingValues lists every field in the bundle that may hold a
// reference.
func (b Bundle) referencingValues() []referencingValue {
	var items []referencingValue
	for _, record := range b.Dns {
		for _, value := range record.Values {
			items = append(items, referencingValue{key: record.Key(), provider: record.Provider, value: value})
		}
	}
	for _, check := range b.HealthChecks {
		items = append(items, referencingValue{key: "healthCheck|" + check.Name, provider: check.Provider, value: check.Target})
	}
	return items
}

// pendingSummary renders the plan summary for a record awaiting references.
func pendingSummary(pending []Reference) string {
	parts := make([]string, 0, len(pending))
	for _, ref := range pending {
		parts = append(parts, ref.String())
	}
	return "awaits " + strings.Join(parts, ", ")
}

// providersOf returns, sorted, every provider holding a spec of kind named
// name.
func (b Bundle) providersOf(kind, name string) []string {
	var providers []string
	add := func(provider, itemName string) {
		if itemName == name && !slices.Contains(providers, provider) {
			providers = append(providers, provider)
		}
	}
	switch kind {
	case "staticAddress":
		for _, item := range b.Addresses {
			add(item.Provider, item.Name)
		}
	case "instance":
		for _, item := range b.Instances {
			add(item.Provider, item.Name)
		}
	}
	sort.Strings(providers)
	return providers
}

// referenceEdges lists each reference by the provider of the spec holding
// it and the provider of the spec it names, skipping a name that resolves
// to no provider or to several.
func (b Bundle) referenceEdges() []referenceEdge {
	var edges []referenceEdge
	for _, item := range b.referencingValues() {
		ref, ok, err := parseReference(item.value)
		if err != nil || !ok {
			continue
		}
		if targets := b.providersOf(ref.Kind, ref.Name); len(targets) == 1 {
			edges = append(edges, referenceEdge{from: item.provider, to: targets[0], kind: ref.Kind})
		}
	}
	return edges
}

// referenceEdge is one reference from a spec on provider from to a spec of
// kind on provider to.
type referenceEdge struct {
	from string
	to   string
	kind string
}

// ProviderOrder returns the providers the bundle uses in the order a live
// run plans and applies them: a provider holding a referenced spec comes
// before every provider that references it, and otherwise name order
// holds. Providers that reference each other are an error.
func (b Bundle) ProviderOrder() ([]string, error) {
	providers := b.Providers()
	needs := map[string]map[string]bool{}
	for _, edge := range b.referenceEdges() {
		if edge.from == edge.to {
			continue
		}
		if needs[edge.from] == nil {
			needs[edge.from] = map[string]bool{}
		}
		needs[edge.from][edge.to] = true
	}
	var order []string
	placed := map[string]bool{}
	for len(order) < len(providers) {
		next := ""
		for _, name := range providers {
			if placed[name] {
				continue
			}
			ready := true
			for dep := range needs[name] {
				if !placed[dep] {
					ready = false
					break
				}
			}
			if ready {
				next = name
				break
			}
		}
		if next == "" {
			var cycle []string
			for _, name := range providers {
				if !placed[name] {
					cycle = append(cycle, name)
				}
			}
			return nil, fmt.Errorf("providers %s reference each other, so no apply order exists; break the cycle with a literal value", strings.Join(cycle, " and "))
		}
		order = append(order, next)
		placed[next] = true
	}
	return order, nil
}

// ReferencedKindsOn returns the kinds of provider's specs that any spec in
// the bundle references, so that provider lists them for the run even when
// its own specs would not.
func (b Bundle) ReferencedKindsOn(provider string) map[string]bool {
	kinds := map[string]bool{}
	for _, edge := range b.referenceEdges() {
		if edge.to == provider {
			kinds[edge.kind] = true
		}
	}
	return kinds
}

// BudgetAlert emails the listed addresses when actual or, with Forecast,
// forecasted spend passes Threshold percent of the budget.
type BudgetAlert struct {
	Threshold int64
	Forecast  bool
	Emails    []string
}

// Budget is a monthly cost budget with alerts. Amount is a decimal string.
type Budget struct {
	Provider string
	Name     string
	Amount   string
	Currency string
	Alerts   []BudgetAlert
}

// HealthCheck probes Target from outside the account and emails on failure.
// Target is an address, a hostname, or a reference. ID is the live check
// id, empty in a spec. Alarm reports whether the live alarm exists; a spec
// always wants it.
type HealthCheck struct {
	Provider         string
	ID               string
	Name             string
	Target           string
	Protocol         string
	Port             int64
	Path             string
	Host             string
	Interval         int64
	FailureThreshold int64
	Emails           []string
	Alarm            bool
}

// key renders an alert for set comparison, emails sorted.
func (a BudgetAlert) key() string {
	kind := "actual"
	if a.Forecast {
		kind = "forecast"
	}
	return fmt.Sprintf("%s|%d|%s", kind, a.Threshold, strings.Join(normalized(a.Emails), ","))
}

func alertKeys(alerts []BudgetAlert) []string {
	keys := make([]string, 0, len(alerts))
	for _, a := range alerts {
		keys = append(keys, a.key())
	}
	sort.Strings(keys)
	return keys
}

// sameAmount compares two decimal amounts numerically, so "15", "15.0",
// and "15.00" are equal.
func sameAmount(a, b string) bool {
	return normalizeAmount(a) == normalizeAmount(b)
}

func normalizeAmount(amount string) string {
	whole, fraction, _ := strings.Cut(strings.TrimSpace(amount), ".")
	fraction = strings.TrimRight(fraction, "0")
	whole = strings.TrimLeft(whole, "0")
	if whole == "" {
		whole = "0"
	}
	if fraction == "" {
		return whole
	}
	return whole + "." + fraction
}

// isDecimalAmount reports whether amount is digits with up to two decimals.
func isDecimalAmount(amount string) bool {
	whole, fraction, dotted := strings.Cut(amount, ".")
	if whole == "" || (dotted && (fraction == "" || len(fraction) > 2)) {
		return false
	}
	for _, c := range whole + fraction {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
