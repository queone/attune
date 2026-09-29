package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

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
// document and returns the combined Bundle. Duplicate resource keys are
// rejected. It is the parser behind both directory and store mode.
func loadSources(sources []specSource) (*Bundle, error) {
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
		resourceKey, err := loadSpecInto(bundle, kind, m)
		if err != nil {
			return nil, err
		}
		if keys[resourceKey] {
			return nil, fmt.Errorf("duplicate resource key %q", resourceKey)
		}
		keys[resourceKey] = true
	}
	return bundle, nil
}

func loadSpecInto(bundle *Bundle, kind string, m map[string]any) (string, error) {
	switch kind {
	case "dnsRecordSet":
		if err := rejectUnknown(m, []string{"kind", "zone", "type", "name", "ttl", "values"}, ""); err != nil {
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
		item := DnsRecord{Zone: zone, RecordType: recordType, Name: name, TTL: ttl, Values: values}
		bundle.Dns = append(bundle.Dns, item)
		return item.Key(), nil

	case "securityGroup":
		if err := rejectUnknown(m, []string{"kind", "name", "owners", "members"}, ""); err != nil {
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
		item := SecurityGroup{Name: name, Owners: owners, Members: members}
		bundle.Groups = append(bundle.Groups, item)
		return "securityGroup|" + item.Name, nil

	case "appRegistration":
		if err := rejectUnknown(m, []string{"kind", "name", "owners", "servicePrincipal"}, ""); err != nil {
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
		servicePrincipal, err := optionalBool(m, "servicePrincipal")
		if err != nil {
			return "", err
		}
		item := AppRegistration{Name: name, Owners: owners, ServicePrincipal: servicePrincipal != nil && *servicePrincipal}
		bundle.Apps = append(bundle.Apps, item)
		return "appRegistration|" + item.Name, nil

	case "roleDefinition":
		if err := rejectUnknown(m, []string{"kind", "name", "description", "assignableScopes", "actions", "notActions", "dataActions", "notDataActions"}, ""); err != nil {
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
		if err := rejectUnknown(m, []string{"kind", "principal", "principalType", "role", "scope"}, ""); err != nil {
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
		item := RoleAssignment{Principal: principal, PrincipalType: principalType, Role: role, Scope: scope}
		bundle.RoleAssignments = append(bundle.RoleAssignments, item)
		return fmt.Sprintf("roleAssignment|%s|%s|%+v", item.Principal, item.Role, item.Scope), nil

	case "resourceGroup":
		if err := rejectUnknown(m, []string{"kind", "name", "location", "tags"}, ""); err != nil {
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
		item := ResourceGroup{Name: name, Location: location, Tags: tags}
		bundle.ResourceGroups = append(bundle.ResourceGroups, item)
		return "resourceGroup|" + item.Name, nil

	default:
		return "", fmt.Errorf("unknown kind %q", kind)
	}
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
