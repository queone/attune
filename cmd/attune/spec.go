package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// --- shared low-level YAML-mapping helpers (used by spec.go and config.go) ---

func mapping(value any, context string) (map[string]any, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a mapping", context)
	}
	return m, nil
}

// rejectUnknown fails if map contains any key not in allowed. context, when
// non-empty, is prefixed onto the error message ("<context>: unknown field
// %q"); when empty, the message is just "unknown field %q" (spec.go's
// per-kind validation has no context; config.go's does).
func rejectUnknown(m map[string]any, allowed []string, context string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			if context == "" {
				return fmt.Errorf("unknown field %q", k)
			}
			return fmt.Errorf("%s: unknown field %q", context, k)
		}
	}
	return nil
}

func optionalString(m map[string]any, name string) (string, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return s, nil
}

func optionalBool(m map[string]any, name string) (*bool, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return nil, nil
	}
	b, ok := v.(bool)
	if !ok {
		return nil, fmt.Errorf("%s must be a boolean", name)
	}
	return &b, nil
}

// --- spec-specific helpers ---

func requiredString(m map[string]any, name string) (string, error) {
	value, err := optionalString(m, name)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func requiredI64(m map[string]any, name string) (int64, error) {
	v, ok := m[name]
	if !ok {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	switch n := v.(type) {
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be an integer", name)
	}
}

func requiredStrings(m map[string]any, name string) ([]string, error) {
	if _, ok := m[name]; !ok {
		return nil, fmt.Errorf("%s is required", name)
	}
	return optionalStrings(m, name)
}

func optionalStrings(m map[string]any, name string) ([]string, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	out := make([]string, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s entries must be strings", name)
		}
		out[i] = s
	}
	return out, nil
}

func optionalStringMap(m map[string]any, name string) (map[string]string, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return map[string]string{}, nil
	}
	inner, err := mapping(v, name)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(inner))
	for k, val := range inner {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%s values must be strings", name)
		}
		out[k] = s
	}
	return out, nil
}

func requiredScope(m map[string]any, name string) (Scope, error) {
	v, ok := m[name]
	if !ok {
		return Scope{}, fmt.Errorf("%s is required", name)
	}
	return parseScope(v)
}

func optionalScopes(m map[string]any, name string) ([]Scope, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	out := make([]Scope, len(items))
	for i, item := range items {
		scope, err := parseScope(item)
		if err != nil {
			return nil, err
		}
		out[i] = scope
	}
	return out, nil
}

func parseScope(value any) (Scope, error) {
	m, err := mapping(value, "scope")
	if err != nil {
		return Scope{}, err
	}
	if err := rejectUnknown(m, []string{"managementGroup", "subscription", "resourceGroup", "dnsZone"}, ""); err != nil {
		return Scope{}, err
	}
	managementGroup, err := optionalString(m, "managementGroup")
	if err != nil {
		return Scope{}, err
	}
	subscription, err := optionalString(m, "subscription")
	if err != nil {
		return Scope{}, err
	}
	resourceGroup, err := optionalString(m, "resourceGroup")
	if err != nil {
		return Scope{}, err
	}
	dnsZone, err := optionalString(m, "dnsZone")
	if err != nil {
		return Scope{}, err
	}
	return Scope{
		ManagementGroup: managementGroup,
		Subscription:    subscription,
		ResourceGroup:   resourceGroup,
		DnsZone:         dnsZone,
	}, nil
}

// isDirectoryObjectID reports whether value has the exact UUID shape used
// by Azure directory object IDs (36 chars, hyphens at 8/13/18/23, hex
// elsewhere). It does not validate UUID version/variant bits.
func isDirectoryObjectID(value string) bool {
	if len(value) != 36 {
		return false
	}
	dashes := map[int]bool{8: true, 13: true, 18: true, 23: true}
	for i := 0; i < len(value); i++ {
		if dashes[i] {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !isHexDigit(value[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// specSource is one spec document's content with the name used in errors:
// a file path in directory mode, an entry key in store mode.
type specSource struct {
	name    string
	content []byte
}

// loadSources parses each source as exactly one strictly validated spec
// document and returns the combined Bundle. A spec's optional provider
// field names its registered provider; a spec without one uses
// defaultProvider. A duplicate resource key within one provider is
// rejected. It is the parser behind both directory and store mode.
func loadSources(sources []specSource, defaultProvider string) (*Bundle, error) {
	bundle := &Bundle{}
	keys := map[string]bool{}
	for _, src := range sources {
		var docs []any
		decoder := yaml.NewDecoder(strings.NewReader(string(src.content)))
		for {
			var doc any
			decodeErr := decoder.Decode(&doc)
			if decodeErr == io.EOF {
				break
			}
			if decodeErr != nil {
				return nil, fmt.Errorf("parse %s: %w", src.name, decodeErr)
			}
			docs = append(docs, doc)
		}
		if len(docs) != 1 {
			return nil, fmt.Errorf("each spec file must contain exactly one YAML document")
		}
		m, err := mapping(docs[0], "spec")
		if err != nil {
			return nil, err
		}
		kind, err := requiredString(m, "kind")
		if err != nil {
			return nil, err
		}
		provider, err := optionalString(m, "provider")
		if err != nil {
			return nil, err
		}
		if provider == "" {
			provider = defaultProvider
		}
		if err := checkKindSupport(provider, kind); err != nil {
			return nil, err
		}
		resourceKey, err := loadSpecInto(bundle, kind, provider, m)
		if err != nil {
			return nil, err
		}
		if keys[provider+"|"+resourceKey] {
			return nil, fmt.Errorf("duplicate resource key %q", resourceKey)
		}
		keys[provider+"|"+resourceKey] = true
	}
	if err := checkReferences(bundle); err != nil {
		return nil, err
	}
	if err := checkDeliveries(bundle); err != nil {
		return nil, err
	}
	return bundle, nil
}

// checkReferences validates every reference in the bundle's record values
// offline: its form, that the kind has that readable field, and that the
// named spec exists under the same provider.
func checkReferences(bundle *Bundle) error {
	for _, item := range bundle.referencingValues() {
		ref, ok, err := parseReference(item.value)
		if err != nil {
			return fmt.Errorf("%s %s: %w", item.key, "value", err)
		}
		if !ok {
			continue
		}
		fields, known := referenceOutputs[ref.Kind]
		if !known {
			return fmt.Errorf("%s value %s: kind %q has no readable field", item.key, ref, ref.Kind)
		}
		if !slices.Contains(fields, ref.Field) {
			return fmt.Errorf("%s value %s: %s has no field %q; readable: %s", item.key, ref, ref.Kind, ref.Field, strings.Join(fields, ", "))
		}
		targetProvider, found := bundle.providerOf(ref.Kind, ref.Name)
		if !found {
			return fmt.Errorf("%s value %s: names no %s spec", item.key, ref, ref.Kind)
		}
		if targetProvider != item.provider {
			return fmt.Errorf("%s value %s: target is on provider %q, not %q; a reference must stay on one provider", item.key, ref, targetProvider, item.provider)
		}
	}
	return nil
}

// loadSpecInto parses one spec of kind into bundle, tagging it with
// provider, and returns its resource key.
func loadSpecInto(bundle *Bundle, kind, provider string, m map[string]any) (string, error) {
	switch kind {
	case "dnsRecordSet":
		if err := rejectUnknown(m, []string{"kind", "provider", "zone", "type", "name", "ttl", "values"}, ""); err != nil {
			return "", err
		}
		zone, err := requiredString(m, "zone")
		if err != nil {
			return "", err
		}
		recordType, err := requiredString(m, "type")
		if err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		ttl, err := requiredI64(m, "ttl")
		if err != nil {
			return "", err
		}
		values, err := requiredStrings(m, "values")
		if err != nil {
			return "", err
		}
		if len(values) == 0 {
			return "", fmt.Errorf("dnsRecordSet values must not be empty")
		}
		item := DnsRecord{Provider: provider, Zone: zone, RecordType: recordType, Name: name, TTL: ttl, Values: values}
		bundle.Dns = append(bundle.Dns, item)
		return item.Key(), nil

	case "securityGroup":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "owners", "members"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		owners, err := optionalStrings(m, "owners")
		if err != nil {
			return "", err
		}
		members, err := optionalStrings(m, "members")
		if err != nil {
			return "", err
		}
		item := SecurityGroup{Provider: provider, Name: name, Owners: owners, Members: members}
		bundle.Groups = append(bundle.Groups, item)
		return "securityGroup|" + item.Name, nil

	case "appRegistration":
		item, err := parseAppRegistration(m, provider)
		if err != nil {
			return "", err
		}
		bundle.Apps = append(bundle.Apps, item)
		return item.Key(), nil

	case "roleDefinition":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "description", "assignableScopes", "actions", "notActions", "dataActions", "notDataActions"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		description, err := optionalString(m, "description")
		if err != nil {
			return "", err
		}
		assignableScopes, err := optionalScopes(m, "assignableScopes")
		if err != nil {
			return "", err
		}
		actions, err := optionalStrings(m, "actions")
		if err != nil {
			return "", err
		}
		notActions, err := optionalStrings(m, "notActions")
		if err != nil {
			return "", err
		}
		dataActions, err := optionalStrings(m, "dataActions")
		if err != nil {
			return "", err
		}
		notDataActions, err := optionalStrings(m, "notDataActions")
		if err != nil {
			return "", err
		}
		item := RoleDefinition{
			Provider:         provider,
			Name:             name,
			Description:      description,
			AssignableScopes: assignableScopes,
			Actions:          actions,
			NotActions:       notActions,
			DataActions:      dataActions,
			NotDataActions:   notDataActions,
		}
		bundle.RoleDefinitions = append(bundle.RoleDefinitions, item)
		return "roleDefinition|" + item.Name, nil

	case "roleAssignment":
		if err := rejectUnknown(m, []string{"kind", "provider", "principal", "principalType", "role", "scope"}, ""); err != nil {
			return "", err
		}
		principal, err := requiredString(m, "principal")
		if err != nil {
			return "", err
		}
		principalType, err := optionalString(m, "principalType")
		if err != nil {
			return "", err
		}
		if principalType == "" && !isDirectoryObjectID(principal) {
			return "", fmt.Errorf("principalType is required for a named principal; use group, securityGroup, servicePrincipal, or user")
		}
		role, err := requiredString(m, "role")
		if err != nil {
			return "", err
		}
		scope, err := requiredScope(m, "scope")
		if err != nil {
			return "", err
		}
		item := RoleAssignment{Provider: provider, Principal: principal, PrincipalType: principalType, Role: role, Scope: scope}
		bundle.RoleAssignments = append(bundle.RoleAssignments, item)
		return fmt.Sprintf("roleAssignment|%s|%s|%+v", item.Principal, item.Role, item.Scope), nil

	case "resourceGroup":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "location", "tags"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		location, err := requiredString(m, "location")
		if err != nil {
			return "", err
		}
		tags, err := optionalStringMap(m, "tags")
		if err != nil {
			return "", err
		}
		item := ResourceGroup{Provider: provider, Name: name, Location: location, Tags: tags}
		bundle.ResourceGroups = append(bundle.ResourceGroups, item)
		return "resourceGroup|" + item.Name, nil

	case "iamRole":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "managedPolicies", "inlinePolicies", "instanceProfile"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		managed, err := optionalStrings(m, "managedPolicies")
		if err != nil {
			return "", err
		}
		inline, err := optionalAnyMap(m, "inlinePolicies")
		if err != nil {
			return "", err
		}
		profile, err := optionalBool(m, "instanceProfile")
		if err != nil {
			return "", err
		}
		item := IamRole{Provider: provider, Name: name, ManagedPolicies: managed, InlinePolicies: inline, InstanceProfile: profile != nil && *profile}
		bundle.IamRoles = append(bundle.IamRoles, item)
		return "iamRole|" + item.Name, nil

	case "bucket":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "region", "publicAccessBlock", "encryption"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		region, err := requiredString(m, "region")
		if err != nil {
			return "", err
		}
		encryption, err := requiredString(m, "encryption")
		if err != nil {
			return "", err
		}
		if encryption != "aes256" && !strings.HasPrefix(encryption, "arn:") {
			return "", fmt.Errorf("bucket encryption must be aes256 or a key ARN, not %q", encryption)
		}
		block := PublicAccessBlock{BlockPublicAcls: true, IgnorePublicAcls: true, BlockPublicPolicy: true, RestrictPublicBuckets: true}
		if raw, ok := m["publicAccessBlock"]; ok && raw != nil {
			pm, err := mapping(raw, "publicAccessBlock")
			if err != nil {
				return "", err
			}
			if err := rejectUnknown(pm, []string{"blockPublicAcls", "ignorePublicAcls", "blockPublicPolicy", "restrictPublicBuckets"}, "publicAccessBlock"); err != nil {
				return "", err
			}
			for key, target := range map[string]*bool{"blockPublicAcls": &block.BlockPublicAcls, "ignorePublicAcls": &block.IgnorePublicAcls, "blockPublicPolicy": &block.BlockPublicPolicy, "restrictPublicBuckets": &block.RestrictPublicBuckets} {
				v, err := optionalBool(pm, key)
				if err != nil {
					return "", err
				}
				if v != nil {
					*target = *v
				}
			}
		}
		item := Bucket{Provider: provider, Name: name, Region: region, PublicAccessBlock: block, Encryption: encryption}
		bundle.Buckets = append(bundle.Buckets, item)
		return "bucket|" + item.Name, nil

	case "firewall":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "vpc", "rules"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		vpc, err := optionalString(m, "vpc")
		if err != nil {
			return "", err
		}
		rawRules, ok := m["rules"].([]any)
		if !ok {
			return "", fmt.Errorf("firewall rules must be a list")
		}
		var rules []FirewallRule
		for i, raw := range rawRules {
			rm, err := mapping(raw, fmt.Sprintf("rules[%d]", i))
			if err != nil {
				return "", err
			}
			if err := rejectUnknown(rm, []string{"protocol", "fromPort", "toPort", "sources"}, fmt.Sprintf("rules[%d]", i)); err != nil {
				return "", err
			}
			protocol, err := requiredString(rm, "protocol")
			if err != nil {
				return "", err
			}
			from, err := requiredI64(rm, "fromPort")
			if err != nil {
				return "", err
			}
			to, err := requiredI64(rm, "toPort")
			if err != nil {
				return "", err
			}
			sources, err := requiredStrings(rm, "sources")
			if err != nil {
				return "", err
			}
			if len(sources) == 0 {
				return "", fmt.Errorf("firewall rule sources must not be empty")
			}
			rules = append(rules, FirewallRule{Protocol: protocol, FromPort: from, ToPort: to, Sources: sources})
		}
		item := Firewall{Provider: provider, Name: name, VPC: vpc, Rules: rules}
		bundle.Firewalls = append(bundle.Firewalls, item)
		return "firewall|" + item.Name, nil

	case "instance":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "type", "image", "role", "firewall", "imdsv2Required", "rootDisk", "tags"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		instanceType, err := requiredString(m, "type")
		if err != nil {
			return "", err
		}
		image, err := requiredString(m, "image")
		if err != nil {
			return "", err
		}
		role, err := optionalString(m, "role")
		if err != nil {
			return "", err
		}
		firewall, err := optionalString(m, "firewall")
		if err != nil {
			return "", err
		}
		imds, err := optionalBool(m, "imdsv2Required")
		if err != nil {
			return "", err
		}
		rawDisk, ok := m["rootDisk"]
		if !ok || rawDisk == nil {
			return "", fmt.Errorf("instance rootDisk is required")
		}
		dm, err := mapping(rawDisk, "rootDisk")
		if err != nil {
			return "", err
		}
		if err := rejectUnknown(dm, []string{"size", "type", "encrypted"}, "rootDisk"); err != nil {
			return "", err
		}
		size, err := requiredI64(dm, "size")
		if err != nil {
			return "", err
		}
		diskType, err := optionalString(dm, "type")
		if err != nil {
			return "", err
		}
		if diskType == "" {
			diskType = "gp3"
		}
		encrypted, err := optionalBool(dm, "encrypted")
		if err != nil {
			return "", err
		}
		tags, err := optionalStringMap(m, "tags")
		if err != nil {
			return "", err
		}
		item := Instance{
			Provider: provider, Name: name, Type: instanceType, Image: image, Role: role, Firewall: firewall,
			Imdsv2Required: imds == nil || *imds,
			RootDisk:       RootDisk{Size: size, Type: diskType, Encrypted: encrypted == nil || *encrypted},
			Tags:           tags,
		}
		bundle.Instances = append(bundle.Instances, item)
		return "instance|" + item.Name, nil

	case "budget":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "amount", "currency", "alerts"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		amount := fmt.Sprintf("%v", m["amount"])
		if m["amount"] == nil || !isDecimalAmount(amount) {
			return "", fmt.Errorf("budget amount must be a decimal such as 15 or 15.00, not %q", amount)
		}
		currency, err := optionalString(m, "currency")
		if err != nil {
			return "", err
		}
		if currency == "" {
			currency = "USD"
		}
		rawAlerts, ok := m["alerts"].([]any)
		if !ok || len(rawAlerts) == 0 {
			return "", fmt.Errorf("budget alerts must be a list with at least one alert")
		}
		var alerts []BudgetAlert
		for i, raw := range rawAlerts {
			am, err := mapping(raw, fmt.Sprintf("alerts[%d]", i))
			if err != nil {
				return "", err
			}
			if err := rejectUnknown(am, []string{"threshold", "forecast", "emails"}, fmt.Sprintf("alerts[%d]", i)); err != nil {
				return "", err
			}
			threshold, err := requiredI64(am, "threshold")
			if err != nil {
				return "", err
			}
			if threshold < 1 || threshold > 1000 {
				return "", fmt.Errorf("budget alert threshold must be a percent from 1 to 1000, not %d", threshold)
			}
			forecast, err := optionalBool(am, "forecast")
			if err != nil {
				return "", err
			}
			emails, err := requiredStrings(am, "emails")
			if err != nil {
				return "", err
			}
			if len(emails) == 0 {
				return "", fmt.Errorf("budget alert emails must not be empty")
			}
			alerts = append(alerts, BudgetAlert{Threshold: threshold, Forecast: forecast != nil && *forecast, Emails: emails})
		}
		item := Budget{Provider: provider, Name: name, Amount: amount, Currency: currency, Alerts: alerts}
		bundle.Budgets = append(bundle.Budgets, item)
		return "budget|" + item.Name, nil

	case "healthCheck":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "target", "protocol", "port", "path", "host", "interval", "failureThreshold", "emails"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		target, err := requiredString(m, "target")
		if err != nil {
			return "", err
		}
		protocol, err := requiredString(m, "protocol")
		if err != nil {
			return "", err
		}
		if protocol != "HTTP" && protocol != "HTTPS" && protocol != "TCP" {
			return "", fmt.Errorf("healthCheck protocol must be HTTP, HTTPS, or TCP, not %q", protocol)
		}
		port, err := requiredI64(m, "port")
		if err != nil {
			return "", err
		}
		if port < 1 || port > 65535 {
			return "", fmt.Errorf("healthCheck port must be from 1 to 65535, not %d", port)
		}
		path, err := optionalString(m, "path")
		if err != nil {
			return "", err
		}
		if protocol == "TCP" && path != "" {
			return "", fmt.Errorf("healthCheck path has no meaning with protocol TCP")
		}
		if protocol != "TCP" && path == "" {
			path = "/"
		}
		host, err := optionalString(m, "host")
		if err != nil {
			return "", err
		}
		if host != "" && !isAddressOrReference(target) {
			return "", fmt.Errorf("healthCheck host is accepted only when target is an address or a reference; a hostname target already names the host")
		}
		interval := int64(30)
		if raw, ok := m["interval"]; ok && raw != nil {
			if interval, err = requiredI64(m, "interval"); err != nil {
				return "", err
			}
		}
		if interval != 10 && interval != 30 {
			return "", fmt.Errorf("healthCheck interval must be 10 or 30, not %d", interval)
		}
		threshold := int64(3)
		if raw, ok := m["failureThreshold"]; ok && raw != nil {
			if threshold, err = requiredI64(m, "failureThreshold"); err != nil {
				return "", err
			}
		}
		if threshold < 1 || threshold > 10 {
			return "", fmt.Errorf("healthCheck failureThreshold must be from 1 to 10, not %d", threshold)
		}
		emails, err := requiredStrings(m, "emails")
		if err != nil {
			return "", err
		}
		if len(emails) == 0 {
			return "", fmt.Errorf("healthCheck emails must not be empty")
		}
		item := HealthCheck{Provider: provider, Name: name, Target: target, Protocol: protocol, Port: port, Path: path, Host: host,
			Interval: interval, FailureThreshold: threshold, Emails: emails, Alarm: true}
		bundle.HealthChecks = append(bundle.HealthChecks, item)
		return "healthCheck|" + item.Name, nil

	case "staticAddress":
		if err := rejectUnknown(m, []string{"kind", "provider", "name", "instance"}, ""); err != nil {
			return "", err
		}
		name, err := requiredString(m, "name")
		if err != nil {
			return "", err
		}
		instance, err := requiredString(m, "instance")
		if err != nil {
			return "", err
		}
		item := StaticAddress{Provider: provider, Name: name, Instance: instance}
		bundle.Addresses = append(bundle.Addresses, item)
		return "staticAddress|" + item.Name, nil

	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
}

// optionalAnyMap reads an optional mapping whose values are kept as parsed,
// for policy documents.
func optionalAnyMap(m map[string]any, name string) (map[string]any, error) {
	raw, ok := m[name]
	if !ok || raw == nil {
		return nil, nil
	}
	return mapping(raw, name)
}

func collectSpecPaths(directory string, paths *[]string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read spec directory: %w", err)
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect spec path: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		if entry.IsDir() {
			if err := collectSpecPaths(path, paths); err != nil {
				return err
			}
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext == ".yaml" || ext == ".yml" {
			*paths = append(*paths, path)
		}
	}
	return nil
}

// isAddressOrReference reports whether a health check target is an IP
// address or a reference rather than a hostname.
func isAddressOrReference(target string) bool {
	if strings.HasPrefix(target, "${") {
		return true
	}
	if strings.Contains(target, ":") {
		return true
	}
	parts := strings.Split(target, ".")
	if len(parts) != 4 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, c := range part {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// parseAppRegistration parses one appRegistration spec. The sign-in fields
// and secrets are optional and unmanaged when absent; tenant names another
// directory than the signed-in one.
func parseAppRegistration(m map[string]any, provider string) (AppRegistration, error) {
	allowed := []string{"kind", "provider", "name", "owners", "servicePrincipal", "tenant", "signInAudience", "redirectUris", "idTokenIssuance", "secrets"}
	if err := rejectUnknown(m, allowed, ""); err != nil {
		return AppRegistration{}, err
	}
	name, err := requiredString(m, "name")
	if err != nil {
		return AppRegistration{}, err
	}
	owners, err := optionalStrings(m, "owners")
	if err != nil {
		return AppRegistration{}, err
	}
	servicePrincipal, err := optionalBool(m, "servicePrincipal")
	if err != nil {
		return AppRegistration{}, err
	}
	tenant, err := optionalString(m, "tenant")
	if err != nil {
		return AppRegistration{}, err
	}
	audience, err := optionalString(m, "signInAudience")
	if err != nil {
		return AppRegistration{}, err
	}
	if audience != "" && !slices.Contains(signInAudiences, audience) {
		return AppRegistration{}, fmt.Errorf("signInAudience must be one of %s", strings.Join(signInAudiences, ", "))
	}
	uris, err := optionalStrings(m, "redirectUris")
	if err != nil {
		return AppRegistration{}, err
	}
	for _, uri := range uris {
		if !isRedirectURI(uri) {
			return AppRegistration{}, fmt.Errorf("redirectUris entry %q must be an absolute https:// URL or http://localhost", uri)
		}
	}
	idTokens, err := optionalBool(m, "idTokenIssuance")
	if err != nil {
		return AppRegistration{}, err
	}
	secrets, err := optionalSecrets(m, "secrets")
	if err != nil {
		return AppRegistration{}, err
	}
	return AppRegistration{
		Provider:         provider,
		Name:             name,
		Owners:           owners,
		ServicePrincipal: servicePrincipal != nil && *servicePrincipal,
		Tenant:           tenant,
		SignInAudience:   audience,
		RedirectURIs:     uris,
		IDTokenIssuance:  idTokens,
		Secrets:          secrets,
	}, nil
}

// isRedirectURI accepts an absolute https:// URL, or http://localhost with
// any port and path, as a web redirect URI.
func isRedirectURI(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		return u.Hostname() == "localhost"
	}
	return false
}

// optionalSecrets parses the secrets list: nil when absent, a non-nil list
// otherwise. Each entry needs a unique name; validForDays defaults to the
// two-year maximum, and issuedAfter is a date or RFC 3339 time (YAML
// reads a bare date as a timestamp) before which a live credential counts
// as stale.
func optionalSecrets(m map[string]any, name string) ([]Secret, error) {
	v, ok := m[name]
	if !ok || v == nil {
		return nil, nil
	}
	items, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", name)
	}
	out := make([]Secret, 0, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		entry, err := mapping(item, fmt.Sprintf("%s[%d]", name, i))
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(entry, []string{"name", "validForDays", "issuedAfter", "deliverTo"}, fmt.Sprintf("%s[%d]", name, i)); err != nil {
			return nil, err
		}
		secretName, err := requiredString(entry, "name")
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", name, i, err)
		}
		if seen[secretName] {
			return nil, fmt.Errorf("%s names %q twice", name, secretName)
		}
		seen[secretName] = true
		secret := Secret{Name: secretName, ValidForDays: secretMaxDays}
		if _, ok := entry["validForDays"]; ok {
			days, err := requiredI64(entry, "validForDays")
			if err != nil {
				return nil, fmt.Errorf("%s %q: %w", name, secretName, err)
			}
			if days < 1 || days > secretMaxDays {
				return nil, fmt.Errorf("%s %q validForDays must be between 1 and %d", name, secretName, secretMaxDays)
			}
			secret.ValidForDays = int(days)
		}
		switch after := entry["issuedAfter"].(type) {
		case nil:
		case time.Time:
			secret.IssuedAfter = after.UTC()
		case string:
			t, err := parseTime(after)
			if err != nil {
				return nil, fmt.Errorf("%s %q issuedAfter must be a date (2006-01-02) or an RFC 3339 time", name, secretName)
			}
			secret.IssuedAfter = t
		default:
			return nil, fmt.Errorf("%s %q issuedAfter must be a date (2006-01-02) or an RFC 3339 time", name, secretName)
		}
		if raw, ok := entry["deliverTo"]; ok {
			delivery, err := parseDelivery(raw)
			if err != nil {
				return nil, fmt.Errorf("%s %q deliverTo: %w", name, secretName, err)
			}
			secret.DeliverTo = delivery
		}
		out = append(out, secret)
	}
	return out, nil
}

// parseDelivery parses a secret's deliverTo mapping: a registered provider
// that accepts secret deliveries, and its one target field.
func parseDelivery(raw any) (*Delivery, error) {
	m, err := mapping(raw, "deliverTo")
	if err != nil {
		return nil, err
	}
	provider, err := requiredString(m, "provider")
	if err != nil {
		return nil, err
	}
	spec, err := LookupProvider(provider)
	if err != nil {
		return nil, err
	}
	if spec.SecretTarget == "" {
		return nil, fmt.Errorf("provider %s does not accept secret deliveries", provider)
	}
	if err := rejectUnknown(m, []string{"provider", spec.SecretTarget}, ""); err != nil {
		return nil, err
	}
	target, err := requiredString(m, spec.SecretTarget)
	if err != nil {
		return nil, err
	}
	if spec.SecretTarget == "parameter" {
		if err := checkParameterName(target); err != nil {
			return nil, err
		}
	}
	return &Delivery{Provider: provider, Field: spec.SecretTarget, Target: target}, nil
}

// checkParameterName applies Parameter Store's naming rules: 1 to 2048
// characters of letters, digits, _ . - and /, a leading / on any
// hierarchical name, and no aws or ssm prefix.
func checkParameterName(name string) error {
	if name == "" || len(name) > 2048 {
		return fmt.Errorf("parameter must be 1 to 2048 characters")
	}
	for _, c := range name {
		if !(c < 128 && (isASCIIAlnum(byte(c)) || strings.ContainsRune("_.-/", c))) {
			return fmt.Errorf("parameter %q may hold only letters, digits, _, ., -, and /", name)
		}
	}
	if strings.Contains(name, "/") && !strings.HasPrefix(name, "/") {
		return fmt.Errorf("parameter %q has a path and must start with /", name)
	}
	first := strings.ToLower(strings.TrimPrefix(name, "/"))
	if strings.HasPrefix(first, "aws") || strings.HasPrefix(first, "ssm") {
		return fmt.Errorf("parameter %q may not start with aws or ssm", name)
	}
	return nil
}

// checkDeliveries refuses two secrets in the store that deliver to the same
// target.
func checkDeliveries(bundle *Bundle) error {
	seen := map[string]string{}
	for _, app := range bundle.Apps {
		for _, secret := range app.Secrets {
			if secret.DeliverTo == nil {
				continue
			}
			key := secret.DeliverTo.String()
			owner := app.Key() + " secret " + secret.Name
			if other, ok := seen[key]; ok {
				return fmt.Errorf("%s and %s both deliver to %s", other, owner, key)
			}
			seen[key] = owner
		}
	}
	return nil
}

// parseTime accepts a date or an RFC 3339 time, both read as UTC.
func parseTime(value string) (time.Time, error) {
	if t, err := time.Parse("2006-01-02", value); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, value)
}
