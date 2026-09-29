package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	armBase   = "https://management.azure.com"
	graphBase = "https://graph.microsoft.com/v1.0"
	dnsAPI    = "2018-05-01"
	groupAPI  = "2021-04-01"
	roleAPI   = "2022-04-01"
)

// Account is the authenticated Azure CLI identity attune runs as.
type Account struct {
	Tenant       string
	Subscription string
	Identity     string
}

// AzureCli authenticates against Azure via the local `az` CLI.
type AzureCli interface {
	Account() (Account, error)
	Token(resource string) (string, error)
}

// CommandAzureCli shells out to the `az` CLI for account/token lookups.
type CommandAzureCli struct{}

func (CommandAzureCli) Account() (Account, error) {
	out, err := exec.Command("az", "account", "show", "--output", "json").Output()
	if err != nil {
		return Account{}, fmt.Errorf("run Azure CLI account lookup: %w; run `az login` and retry", err)
	}
	var value map[string]any
	if err := json.Unmarshal(out, &value); err != nil {
		return Account{}, fmt.Errorf("azure CLI returned malformed account data")
	}
	identity := "authenticated"
	if user, ok := value["user"].(map[string]any); ok {
		if name, ok := user["name"].(string); ok && name != "" {
			identity = name
		}
	}
	return Account{
		Tenant:       jsonString(value, "tenantId"),
		Subscription: jsonString(value, "id"),
		Identity:     identity,
	}, nil
}

func (CommandAzureCli) Token(resource string) (string, error) {
	out, err := exec.Command("az", "account", "get-access-token",
		"--resource", resource, "--query", "accessToken", "--output", "tsv").Output()
	if err != nil {
		return "", fmt.Errorf("run Azure CLI token lookup: %w; run `az login` and retry", err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("azure CLI returned an empty access token; run `az login` and retry")
	}
	return token, nil
}

// HTTPRequest is a provider REST request, abstracted for testability.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers [][2]string
	Body    []byte
}

// HTTPResponse is a provider REST response.
type HTTPResponse struct {
	Status int
	Body   []byte
}

// HTTPTransport sends an HTTPRequest and returns its HTTPResponse.
type HTTPTransport interface {
	Send(req *HTTPRequest) (*HTTPResponse, error)
}

// TCPHTTPTransport is the real network implementation of HTTPTransport.
type TCPHTTPTransport struct{}

func (TCPHTTPTransport) Send(req *HTTPRequest) (*HTTPResponse, error) {
	var bodyReader io.Reader
	if req.Body != nil {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequest(req.Method, req.URL, bodyReader)
	if err != nil {
		return nil, err
	}
	for _, h := range req.Headers {
		httpReq.Header.Set(h[0], h[1])
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &HTTPResponse{Status: resp.StatusCode, Body: body}, nil
}

// AzureProvider implements Provider against live Azure ARM and Microsoft
// Graph REST APIs.
type AzureProvider struct {
	cli           AzureCli
	transport     HTTPTransport
	Subscription  string
	resourceGroup string
	graphToken    string
	graphTokenSet bool
	armToken      string
	armTokenSet   bool
}

// NewAzureProvider builds a provider using the real `az` CLI and network transport.
func NewAzureProvider(subscription, resourceGroup string) *AzureProvider {
	return NewAzureProviderWith(CommandAzureCli{}, TCPHTTPTransport{}, subscription, resourceGroup)
}

// NewAzureProviderWith builds a provider with injected AzureCli/HTTPTransport, for testing.
func NewAzureProviderWith(cli AzureCli, transport HTTPTransport, subscription, resourceGroup string) *AzureProvider {
	return &AzureProvider{cli: cli, transport: transport, Subscription: subscription, resourceGroup: resourceGroup}
}

// Ground authenticates and, if no subscription was configured, adopts the
// authenticated account's default subscription.
func (p *AzureProvider) Ground() (Account, error) {
	account, err := p.cli.Account()
	if err != nil {
		return Account{}, err
	}
	if p.Subscription == "" {
		p.Subscription = account.Subscription
	}
	return account, nil
}

func (p *AzureProvider) requireDNSTarget() error {
	if p.Subscription == "" {
		return fmt.Errorf("azure subscription is required; set -S or ARM_SUBSCRIPTION_ID")
	}
	if p.resourceGroup == "" {
		return fmt.Errorf("azure resource group is required; set -g or ARM_RESOURCE_GROUP")
	}
	return nil
}

// send authorizes and performs one provider request, returning the raw
// response. It refuses to attach the bearer token to any URL outside the
// two known provider origins.
func (p *AzureProvider) send(method, url string, body any) (*HTTPResponse, error) {
	graph := strings.HasPrefix(url, "https://graph.microsoft.com/")
	allowed := graph || strings.HasPrefix(url, "https://management.azure.com/")
	if !allowed {
		return nil, fmt.Errorf("refuse credential transmission to an unsupported origin")
	}
	var token string
	if graph {
		if !p.graphTokenSet {
			t, err := p.cli.Token("https://graph.microsoft.com")
			if err != nil {
				return nil, err
			}
			p.graphToken, p.graphTokenSet = t, true
		}
		token = p.graphToken
	} else {
		if !p.armTokenSet {
			t, err := p.cli.Token("https://management.azure.com")
			if err != nil {
				return nil, err
			}
			p.armToken, p.armTokenSet = t, true
		}
		token = p.armToken
	}
	var encoded []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		encoded = b
	}
	headers := [][2]string{
		{"Authorization", "Bearer " + token},
		{"Accept", "application/json"},
	}
	if encoded != nil {
		headers = append(headers, [2]string{"Content-Type", "application/json"})
	}
	resp, err := p.transport.Send(&HTTPRequest{Method: method, URL: url, Headers: headers, Body: encoded})
	if err != nil {
		return nil, fmt.Errorf("send provider request: %w", err)
	}
	return resp, nil
}

// statusError renders a non-2xx provider response as a redacted error.
func statusError(resp *HTTPResponse) error {
	detail := sanitizeBody(resp.Body)
	if detail == "" {
		return fmt.Errorf("provider request failed with HTTP %d", resp.Status)
	}
	return fmt.Errorf("provider request failed with HTTP %d: %s", resp.Status, detail)
}

// request sends an authenticated ARM or Graph REST request. It refuses to
// attach the bearer token to any URL outside the two known provider
// origins, and redacts non-2xx response bodies before they reach any
// diagnostic or error message.
func (p *AzureProvider) request(method, url string, body any) (any, error) {
	resp, err := p.send(method, url, body)
	if err != nil {
		return nil, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil, statusError(resp)
	}
	if len(resp.Body) == 0 {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal(resp.Body, &value); err != nil {
		return nil, fmt.Errorf("provider returned malformed JSON")
	}
	return value, nil
}

// pages follows nextLink/@odata.nextLink until exhausted, concatenating
// every page's "value" array.
func (p *AzureProvider) pages(url string) ([]any, error) {
	var values []any
	for url != "" {
		page, err := p.request("GET", url, nil)
		if err != nil {
			return nil, err
		}
		obj, _ := page.(map[string]any)
		if items, ok := obj["value"].([]any); ok {
			values = append(values, items...)
		}
		url = ""
		if next, ok := obj["nextLink"].(string); ok && next != "" {
			url = next
		} else if next, ok := obj["@odata.nextLink"].(string); ok && next != "" {
			url = next
		}
	}
	return values, nil
}

func (p *AzureProvider) findID(collection, field, value string) (string, error) {
	filter := percentEncode(fmt.Sprintf("%s eq '%s'", field, value))
	url := fmt.Sprintf("%s/%s?$select=id&$filter=%s", graphBase, collection, filter)
	result, err := p.request("GET", url, nil)
	if err != nil {
		return "", err
	}
	obj, _ := result.(map[string]any)
	items, _ := obj["value"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, _ := items[0].(map[string]any)
	id, _ := first["id"].(string)
	return id, nil
}

func (p *AzureProvider) listRefs(path string, includeServicePrincipals bool) ([]string, error) {
	items, err := p.pages(fmt.Sprintf("%s%s?$select=id", graphBase, path))
	if err != nil {
		return nil, err
	}
	ids := extractIDs(items)
	if includeServicePrincipals {
		spItems, err := p.pages(fmt.Sprintf("%s%s/microsoft.graph.servicePrincipal?$select=id", graphBase, path))
		if err != nil {
			return nil, err
		}
		ids = append(ids, extractIDs(spItems)...)
	}
	return sortedUnique(ids), nil
}

func extractIDs(items []any) []string {
	var ids []string
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			if id, ok := obj["id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func (p *AzureProvider) syncRefs(path string, desired []string) error {
	current, err := p.listRefs(path, false)
	if err != nil {
		return err
	}
	currentSorted := sortedUnique(current)
	desiredSorted := sortedUnique(desired)
	currentSet := toStringSet(currentSorted)
	desiredSet := toStringSet(desiredSorted)
	for _, id := range desiredSorted {
		if !currentSet[id] {
			if _, err := p.request("POST", fmt.Sprintf("%s%s/$ref", graphBase, path),
				map[string]any{"@odata.id": fmt.Sprintf("%s/directoryObjects/%s", graphBase, id)}); err != nil {
				return err
			}
		}
	}
	for _, id := range currentSorted {
		if !desiredSet[id] {
			if _, err := p.request("DELETE", fmt.Sprintf("%s%s/%s/$ref", graphBase, path, id), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func toStringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func (p *AzureProvider) listNames(collection string) ([]string, error) {
	items, err := p.pages(fmt.Sprintf("%s/%s?$select=displayName&$top=999", graphBase, collection))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, item := range items {
		if obj, ok := item.(map[string]any); ok {
			if name, ok := obj["displayName"].(string); ok {
				names = append(names, name)
			}
		}
	}
	return names, nil
}

// --- Provider interface implementation ---

func (p *AzureProvider) EnsureZone(zone string) error {
	if err := p.requireDNSTarget(); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s?api-version=%s",
		armBase, pct(p.Subscription), pct(p.resourceGroup), pct(zone), dnsAPI)
	_, err := p.request("PUT", url, map[string]any{"location": "global"})
	return err
}

// HasZone reports whether the DNS zone exists in the target resource
// group, distinguishing a missing zone from any other provider failure.
func (p *AzureProvider) HasZone(zone string) (bool, error) {
	if err := p.requireDNSTarget(); err != nil {
		return false, err
	}
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s?api-version=%s",
		armBase, pct(p.Subscription), pct(p.resourceGroup), pct(zone), dnsAPI)
	resp, err := p.send("GET", url, nil)
	if err != nil {
		return false, err
	}
	if resp.Status == 404 {
		return false, nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return false, statusError(resp)
	}
	return true, nil
}

func (p *AzureProvider) ListDNS(zone string) ([]DnsRecord, error) {
	if err := p.requireDNSTarget(); err != nil {
		return nil, err
	}
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s/recordsets?api-version=%s",
		armBase, pct(p.Subscription), pct(p.resourceGroup), pct(zone), dnsAPI)
	items, err := p.pages(url)
	if err != nil {
		return nil, err
	}
	var records []DnsRecord
	for _, item := range items {
		if record, ok := decodeDNS(zone, item); ok {
			records = append(records, record)
		}
	}
	return records, nil
}

func (p *AzureProvider) PutDNS(record DnsRecord) error {
	if err := p.requireDNSTarget(); err != nil {
		return err
	}
	body, err := encodeDNS(record)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s/%s/%s?api-version=%s",
		armBase, pct(p.Subscription), pct(p.resourceGroup), pct(record.Zone), pct(strings.ToUpper(record.RecordType)), pct(record.Name), dnsAPI)
	_, err = p.request("PUT", url, body)
	return err
}

func (p *AzureProvider) DeleteDNS(record DnsRecord) error {
	if err := p.requireDNSTarget(); err != nil {
		return err
	}
	url := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/dnsZones/%s/%s/%s?api-version=%s",
		armBase, pct(p.Subscription), pct(p.resourceGroup), pct(record.Zone), pct(strings.ToUpper(record.RecordType)), pct(record.Name), dnsAPI)
	_, err := p.request("DELETE", url, nil)
	return err
}

func (p *AzureProvider) GetGroup(name string) (*SecurityGroup, error) {
	id, err := p.findID("groups", "displayName", name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	owners, err := p.listRefs(fmt.Sprintf("/groups/%s/owners", id), true)
	if err != nil {
		return nil, err
	}
	members, err := p.listRefs(fmt.Sprintf("/groups/%s/members", id), true)
	if err != nil {
		return nil, err
	}
	return &SecurityGroup{Name: name, Owners: owners, Members: members}, nil
}

func (p *AzureProvider) ListGroupNames() ([]string, error) {
	return p.listNames("groups")
}

func (p *AzureProvider) PutGroup(group SecurityGroup) error {
	id, err := p.findID("groups", "displayName", group.Name)
	if err != nil {
		return err
	}
	if id == "" {
		created, err := p.request("POST", graphBase+"/groups", map[string]any{
			"displayName":     group.Name,
			"mailEnabled":     false,
			"mailNickname":    nickname(group.Name),
			"securityEnabled": true,
		})
		if err != nil {
			return err
		}
		obj, _ := created.(map[string]any)
		newID, _ := obj["id"].(string)
		if newID == "" {
			return fmt.Errorf("graph group response omitted id")
		}
		id = newID
	}
	if err := p.syncRefs(fmt.Sprintf("/groups/%s/owners", id), group.Owners); err != nil {
		return err
	}
	return p.syncRefs(fmt.Sprintf("/groups/%s/members", id), group.Members)
}

func (p *AzureProvider) DeleteGroup(name string) error {
	id, err := p.findID("groups", "displayName", name)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	_, err = p.request("DELETE", fmt.Sprintf("%s/groups/%s", graphBase, id), nil)
	return err
}

func (p *AzureProvider) GetApp(name string) (*AppRegistration, error) {
	filter := percentEncode(fmt.Sprintf("displayName eq '%s'", name))
	result, err := p.request("GET", fmt.Sprintf("%s/applications?$select=id,appId&$filter=%s", graphBase, filter), nil)
	if err != nil {
		return nil, err
	}
	obj, _ := result.(map[string]any)
	items, _ := obj["value"].([]any)
	if len(items) == 0 {
		return nil, nil
	}
	item, _ := items[0].(map[string]any)
	id := jsonString(item, "id")
	appID := jsonString(item, "appId")
	owners, err := p.listRefs(fmt.Sprintf("/applications/%s/owners", id), true)
	if err != nil {
		return nil, err
	}
	exists, err := p.servicePrincipalExists(appID)
	if err != nil {
		return nil, err
	}
	return &AppRegistration{Name: name, Owners: owners, ServicePrincipal: exists}, nil
}

func (p *AzureProvider) servicePrincipalExists(appID string) (bool, error) {
	filter := percentEncode(fmt.Sprintf("appId eq '%s'", appID))
	result, err := p.request("GET", fmt.Sprintf("%s/servicePrincipals?$select=id&$filter=%s", graphBase, filter), nil)
	if err != nil {
		return false, err
	}
	obj, _ := result.(map[string]any)
	items, ok := obj["value"].([]any)
	return ok && len(items) > 0, nil
}

func (p *AzureProvider) ListAppNames() ([]string, error) {
	return p.listNames("applications")
}

func (p *AzureProvider) PutApp(app AppRegistration) error {
	filter := percentEncode(fmt.Sprintf("displayName eq '%s'", app.Name))
	found, err := p.request("GET", fmt.Sprintf("%s/applications?$select=id,appId&$filter=%s", graphBase, filter), nil)
	if err != nil {
		return err
	}
	obj, _ := found.(map[string]any)
	items, _ := obj["value"].([]any)
	var id, appID string
	if len(items) > 0 {
		item, _ := items[0].(map[string]any)
		id, appID = jsonString(item, "id"), jsonString(item, "appId")
	} else {
		created, err := p.request("POST", graphBase+"/applications", map[string]any{"displayName": app.Name})
		if err != nil {
			return err
		}
		createdObj, _ := created.(map[string]any)
		id, appID = jsonString(createdObj, "id"), jsonString(createdObj, "appId")
	}
	if err := p.syncRefs(fmt.Sprintf("/applications/%s/owners", id), app.Owners); err != nil {
		return err
	}
	if app.ServicePrincipal {
		exists, err := p.servicePrincipalExists(appID)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := p.request("POST", graphBase+"/servicePrincipals", map[string]any{"appId": appID}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *AzureProvider) DeleteApp(name string) error {
	id, err := p.findID("applications", "displayName", name)
	if err != nil {
		return err
	}
	if id == "" {
		return nil
	}
	_, err = p.request("DELETE", fmt.Sprintf("%s/applications/%s", graphBase, id), nil)
	return err
}

func (p *AzureProvider) ListRoleDefinitions(subscription string) ([]RoleDefinition, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions?api-version=%s&$filter=%s",
		armBase, pct(subscription), roleAPI, percentEncode("type eq 'CustomRole'"))
	items, err := p.pages(url)
	if err != nil {
		return nil, err
	}
	out := make([]RoleDefinition, len(items))
	for i, item := range items {
		out[i] = decodeRole(item)
	}
	return out, nil
}

func (p *AzureProvider) PutRoleDefinition(role RoleDefinition, subscription string) error {
	existingID, _ := p.resolveRole(role.Name, subscription)
	id := ""
	if existingID != "" {
		parts := strings.Split(existingID, "/")
		id = parts[len(parts)-1]
	}
	if id == "" {
		id = deterministicUUID("role|" + role.Name)
	}
	var scopes []string
	if len(role.AssignableScopes) == 0 {
		scopes = []string{fmt.Sprintf("/subscriptions/%s", subscription)}
	} else {
		for _, s := range role.AssignableScopes {
			armID, err := s.ArmID(subscription)
			if err != nil {
				return err
			}
			scopes = append(scopes, armID)
		}
	}
	if len(scopes) == 0 {
		return fmt.Errorf("role definition needs an assignable scope")
	}
	targetScope := scopes[0]
	body := map[string]any{
		"properties": map[string]any{
			"roleName":         role.Name,
			"description":      role.Description,
			"type":             "CustomRole",
			"assignableScopes": scopes,
			"permissions": []any{
				map[string]any{
					"actions":        role.Actions,
					"notActions":     role.NotActions,
					"dataActions":    role.DataActions,
					"notDataActions": role.NotDataActions,
				},
			},
		},
	}
	url := fmt.Sprintf("%s%s/providers/Microsoft.Authorization/roleDefinitions/%s?api-version=%s", armBase, targetScope, id, roleAPI)
	_, err := p.request("PUT", url, body)
	return err
}

func (p *AzureProvider) DeleteRoleDefinition(name, subscription string) error {
	id, err := p.resolveRole(name, subscription)
	if err != nil {
		return err
	}
	_, err = p.request("DELETE", fmt.Sprintf("%s%s?api-version=%s", armBase, id, roleAPI), nil)
	return err
}

func (p *AzureProvider) ResolvePrincipal(name, principalType string) (string, error) {
	if isDirectoryObjectID(name) {
		return name, nil
	}
	var collection string
	switch strings.ToLower(principalType) {
	case "group", "securitygroup":
		collection = "groups"
	case "serviceprincipal":
		collection = "servicePrincipals"
	case "user":
		collection = "users"
	default:
		return "", fmt.Errorf("unknown principalType; use group, securityGroup, servicePrincipal, or user")
	}
	id, err := p.findID(collection, "displayName", name)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", fmt.Errorf("%s was not found", principalType)
	}
	return id, nil
}

func (p *AzureProvider) resolveRole(name, subscription string) (string, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Authorization/roleDefinitions?api-version=%s&$filter=%s",
		armBase, pct(subscription), roleAPI, percentEncode(fmt.Sprintf("roleName eq '%s'", name)))
	result, err := p.request("GET", url, nil)
	if err != nil {
		return "", err
	}
	obj, _ := result.(map[string]any)
	items, _ := obj["value"].([]any)
	if len(items) == 0 {
		return "", fmt.Errorf("role was not found")
	}
	item, _ := items[0].(map[string]any)
	id, _ := item["id"].(string)
	if id == "" {
		return "", fmt.Errorf("role was not found")
	}
	return id, nil
}

// ResolveRole is the exported Provider-interface form of resolveRole.
func (p *AzureProvider) ResolveRole(name, subscription string) (string, error) {
	return p.resolveRole(name, subscription)
}

func (p *AzureProvider) ListRoleAssignments(scope string) ([]LiveAssignment, error) {
	url := fmt.Sprintf("%s%s/providers/Microsoft.Authorization/roleAssignments?api-version=%s", armBase, scope, roleAPI)
	items, err := p.pages(url)
	if err != nil {
		return nil, err
	}
	out := make([]LiveAssignment, len(items))
	for i, item := range items {
		obj, _ := item.(map[string]any)
		out[i] = LiveAssignment{
			ID:          jsonString(obj, "id"),
			PrincipalID: jsonPointerString(obj, "properties", "principalId"),
			RoleID:      jsonPointerString(obj, "properties", "roleDefinitionId"),
			Scope:       firstNonEmpty(jsonPointerString(obj, "properties", "scope"), scope),
		}
	}
	return out, nil
}

func (p *AzureProvider) PutRoleAssignment(principal, role, scope string) error {
	id := deterministicUUID(fmt.Sprintf("%s|%s|%s", scope, principal, role))
	url := fmt.Sprintf("%s%s/providers/Microsoft.Authorization/roleAssignments/%s?api-version=%s", armBase, scope, id, roleAPI)
	_, err := p.request("PUT", url, map[string]any{
		"properties": map[string]any{"roleDefinitionId": role, "principalId": principal},
	})
	return err
}

func (p *AzureProvider) DeleteRoleAssignment(assignment LiveAssignment) error {
	_, err := p.request("DELETE", fmt.Sprintf("%s%s?api-version=%s", armBase, assignment.ID, roleAPI), nil)
	return err
}

func (p *AzureProvider) ListResourceGroups() ([]ResourceGroup, error) {
	url := fmt.Sprintf("%s/subscriptions/%s/resourcegroups?api-version=%s", armBase, pct(p.Subscription), groupAPI)
	items, err := p.pages(url)
	if err != nil {
		return nil, err
	}
	out := make([]ResourceGroup, len(items))
	for i, item := range items {
		obj, _ := item.(map[string]any)
		tags := map[string]string{}
		if tagsObj, ok := obj["tags"].(map[string]any); ok {
			for k, v := range tagsObj {
				if s, ok := v.(string); ok {
					tags[k] = s
				}
			}
		}
		out[i] = ResourceGroup{Name: jsonString(obj, "name"), Location: jsonString(obj, "location"), Tags: tags}
	}
	return out, nil
}

func (p *AzureProvider) PutResourceGroup(group ResourceGroup) error {
	url := fmt.Sprintf("%s/subscriptions/%s/resourcegroups/%s?api-version=%s", armBase, pct(p.Subscription), pct(group.Name), groupAPI)
	_, err := p.request("PUT", url, map[string]any{"location": group.Location, "tags": group.Tags})
	return err
}

func (p *AzureProvider) DeleteResourceGroup(name string) error {
	url := fmt.Sprintf("%s/subscriptions/%s/resourcegroups/%s?api-version=%s", armBase, pct(p.Subscription), pct(name), groupAPI)
	_, err := p.request("DELETE", url, nil)
	return err
}

// --- pure helpers ---

func jsonString(value any, field string) string {
	obj, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := obj[field].(string)
	return s
}

func jsonPointerString(obj map[string]any, field, subfield string) string {
	if obj == nil {
		return ""
	}
	inner, ok := obj[field].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := inner[subfield].(string)
	return s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func pct(value string) string { return percentEncode(value) }

// percentEncode strictly percent-encodes value: only ASCII alphanumerics
// and -._~ pass through unescaped; every other byte becomes %XX (uppercase
// hex). This intentionally diverges from net/url.QueryEscape/PathEscape
// (e.g. QueryEscape turns space into "+", not "%20") — ARM paths and Graph
// $filter values require this exact encoding.
func percentEncode(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if isASCIIAlnum(c) || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// deterministicUUID derives a stable, name-based (v5-style) UUID from value
// using SHA1 over the RFC4122 URL namespace plus value, matching rkit's
// exact algorithm. Role assignments/definitions rkit's attune already
// created live must resolve to the same resource ID here, or apply would
// create duplicates instead of updating them.
func deterministicUUID(value string) string {
	urlNamespace := []byte{
		0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
	}
	namespaced := append(append([]byte{}, urlNamespace...), []byte(value)...)
	digest := sha1.Sum(namespaced)
	var b [16]byte
	copy(b[:], digest[:16])
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		b[0], b[1], b[2], b[3], b[4], b[5], b[6], b[7], b[8], b[9], b[10], b[11], b[12], b[13], b[14], b[15])
}

func nickname(name string) string {
	var b strings.Builder
	for i := 0; i < len(name); i++ {
		c := name[i]
		if isASCIIAlnum(c) {
			b.WriteByte(c)
		}
	}
	if b.Len() == 0 {
		return "group"
	}
	return b.String()
}

// sanitizeBody redacts any non-empty error-response body wholesale, so a
// failure diagnostic can never leak provider response content (including
// credentials) even in part.
func sanitizeBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return "provider response was redacted"
}

func decodeRole(item any) RoleDefinition {
	obj, _ := item.(map[string]any)
	props, _ := obj["properties"].(map[string]any)
	var permission map[string]any
	if perms, ok := props["permissions"].([]any); ok && len(perms) > 0 {
		permission, _ = perms[0].(map[string]any)
	}
	var scopes []Scope
	for _, s := range jsonStrings(props, "assignableScopes") {
		scopes = append(scopes, parseArmScope(s))
	}
	return RoleDefinition{
		Name:             jsonString(props, "roleName"),
		Description:      jsonString(props, "description"),
		AssignableScopes: scopes,
		Actions:          jsonStrings(permission, "actions"),
		NotActions:       jsonStrings(permission, "notActions"),
		DataActions:      jsonStrings(permission, "dataActions"),
		NotDataActions:   jsonStrings(permission, "notDataActions"),
	}
}

func parseArmScope(value string) Scope {
	parts := strings.Split(strings.Trim(value, "/"), "/")
	after := func(name string) string {
		for i := 0; i+1 < len(parts); i++ {
			if strings.EqualFold(parts[i], name) {
				return parts[i+1]
			}
		}
		return ""
	}
	return Scope{
		ManagementGroup: after("managementGroups"),
		Subscription:    after("subscriptions"),
		ResourceGroup:   after("resourceGroups"),
		DnsZone:         after("dnsZones"),
	}
}

func jsonStrings(value any, field string) []string {
	obj, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	items, ok := obj[field].([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// decodeDNS decodes one ARM DNS recordset item. SOA records and unsupported
// types are skipped (ok=false).
func decodeDNS(zone string, item any) (DnsRecord, bool) {
	obj, ok := item.(map[string]any)
	if !ok {
		return DnsRecord{}, false
	}
	name, ok := obj["name"].(string)
	if !ok {
		return DnsRecord{}, false
	}
	typeStr, ok := obj["type"].(string)
	if !ok {
		return DnsRecord{}, false
	}
	typeParts := strings.Split(typeStr, "/")
	recordType := strings.ToUpper(typeParts[len(typeParts)-1])
	if recordType == "SOA" {
		return DnsRecord{}, false
	}
	properties, ok := obj["properties"].(map[string]any)
	if !ok {
		return DnsRecord{}, false
	}
	var ttl int64
	if v, ok := properties["TTL"]; ok {
		switch n := v.(type) {
		case int:
			ttl = int64(n)
		case float64:
			ttl = int64(n)
		}
	}
	var field string
	switch recordType {
	case "A":
		field = "ARecords"
	case "AAAA":
		field = "AAAARecords"
	case "MX":
		field = "MXRecords"
	case "NS":
		field = "NSRecords"
	case "PTR":
		field = "PTRRecords"
	case "SRV":
		field = "SRVRecords"
	case "TXT":
		field = "TXTRecords"
	case "CNAME":
		field = "CNAMERecord"
	default:
		return DnsRecord{}, false
	}
	values := decodeDNSValues(recordType, properties[field])
	return DnsRecord{Zone: zone, RecordType: recordType, Name: name, TTL: ttl, Values: values}, true
}

func decodeDNSValues(kind string, value any) []string {
	if kind == "CNAME" {
		if obj, ok := value.(map[string]any); ok {
			if cname, ok := obj["cname"].(string); ok {
				return []string{cname}
			}
		}
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch kind {
		case "A":
			if v, ok := item["ipv4Address"].(string); ok {
				out = append(out, v)
			}
		case "AAAA":
			if v, ok := item["ipv6Address"].(string); ok {
				out = append(out, v)
			}
		case "NS":
			if v, ok := item["nsdname"].(string); ok {
				out = append(out, v)
			}
		case "PTR":
			if v, ok := item["ptrdname"].(string); ok {
				out = append(out, v)
			}
		case "MX":
			preference, hasPref := item["preference"]
			exchange, hasExch := item["exchange"].(string)
			if prefNum, ok := jsonNumber(preference); ok && hasPref && hasExch {
				out = append(out, fmt.Sprintf("%d %s", prefNum, exchange))
			}
		case "SRV":
			priority, hasP := jsonNumber(item["priority"])
			weight, hasW := jsonNumber(item["weight"])
			port, hasPort := jsonNumber(item["port"])
			target, hasTarget := item["target"].(string)
			if hasP && hasW && hasPort && hasTarget {
				out = append(out, fmt.Sprintf("%d %d %d %s", priority, weight, port, target))
			}
		case "TXT":
			if parts, ok := item["value"].([]any); ok {
				var joined strings.Builder
				for _, p := range parts {
					if s, ok := p.(string); ok {
						joined.WriteString(s)
					}
				}
				out = append(out, joined.String())
			}
		}
	}
	return out
}

func jsonNumber(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	default:
		return 0, false
	}
}

// encodeDNS builds the ARM recordset properties body for record. Only A,
// AAAA, CNAME (single value), NS, PTR, TXT, and MX/SRV are supported.
func encodeDNS(record DnsRecord) (map[string]any, error) {
	values := record.Values
	var data map[string]any
	switch strings.ToUpper(record.RecordType) {
	case "A":
		data = map[string]any{"ARecords": mapValues(values, func(v string) any {
			return map[string]any{"ipv4Address": v}
		})}
	case "AAAA":
		data = map[string]any{"AAAARecords": mapValues(values, func(v string) any {
			return map[string]any{"ipv6Address": v}
		})}
	case "CNAME":
		if len(values) != 1 {
			return nil, fmt.Errorf("unsupported DNS record type or malformed values")
		}
		data = map[string]any{"CNAMERecord": map[string]any{"cname": values[0]}}
	case "NS":
		data = map[string]any{"NSRecords": mapValues(values, func(v string) any {
			return map[string]any{"nsdname": v}
		})}
	case "PTR":
		data = map[string]any{"PTRRecords": mapValues(values, func(v string) any {
			return map[string]any{"ptrdname": v}
		})}
	case "TXT":
		data = map[string]any{"TXTRecords": mapValues(values, func(v string) any {
			return map[string]any{"value": []any{v}}
		})}
	case "MX":
		data = map[string]any{"MXRecords": mapValues(values, func(v string) any {
			parts := strings.Fields(v)
			preference := int64(0)
			exchange := ""
			if len(parts) > 0 {
				if n, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
					preference = n
				}
				exchange = strings.Join(parts[1:], " ")
			}
			return map[string]any{"preference": preference, "exchange": exchange}
		})}
	case "SRV":
		data = map[string]any{"SRVRecords": mapValues(values, func(v string) any {
			parts := strings.Fields(v)
			var priority, weight, port int64
			target := ""
			if len(parts) > 0 {
				priority, _ = strconv.ParseInt(parts[0], 10, 64)
			}
			if len(parts) > 1 {
				weight, _ = strconv.ParseInt(parts[1], 10, 64)
			}
			if len(parts) > 2 {
				port, _ = strconv.ParseInt(parts[2], 10, 64)
			}
			if len(parts) > 3 {
				target = strings.Join(parts[3:], " ")
			}
			return map[string]any{"priority": priority, "weight": weight, "port": port, "target": target}
		})}
	default:
		return nil, fmt.Errorf("unsupported DNS record type or malformed values")
	}
	props := make(map[string]any, len(data)+1)
	maps.Copy(props, data)
	props["TTL"] = record.TTL
	return map[string]any{"properties": props}, nil
}

func mapValues(values []string, f func(string) any) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = f(v)
	}
	return out
}
