package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

type syntheticCli struct{}

func (syntheticCli) Account() (Account, error)            { return Account{}, nil }
func (syntheticCli) Token(string, string) (string, error) { return "synthetic-token", nil }

// syntheticTransport records every request URL (and, when requests is set,
// the full request for header/method inspection) and returns queued
// responses in order (repeating the last one), defaulting to a single
// canned 200 response when no queue is set.
type syntheticTransport struct {
	urls      *[]string
	requests  *[]*HTTPRequest
	responses []*HTTPResponse
	index     int
}

func (t *syntheticTransport) Send(req *HTTPRequest) (*HTTPResponse, error) {
	*t.urls = append(*t.urls, req.URL)
	if t.requests != nil {
		*t.requests = append(*t.requests, req)
	}
	if len(t.responses) == 0 {
		return &HTTPResponse{Status: 200, Body: []byte(`{"value":[{"id":"00000000-0000-0000-0000-000000000002"}]}`)}, nil
	}
	resp := t.responses[t.index]
	if t.index < len(t.responses)-1 {
		t.index++
	}
	return resp, nil
}

func TestSecurityGroupAliasUsesGroupLookupCaseInsensitively(t *testing.T) {
	var urls []string
	provider := NewAzureProviderWith(syntheticCli{}, &syntheticTransport{urls: &urls}, "", "")

	id, err := provider.ResolvePrincipal("synthetic-group", "SeCuRiTyGrOuP")
	if err != nil {
		t.Fatalf("ResolvePrincipal: %v", err)
	}
	if id != "00000000-0000-0000-0000-000000000002" {
		t.Errorf("id = %q, want %q", id, "00000000-0000-0000-0000-000000000002")
	}
	if len(urls) != 1 {
		t.Fatalf("got %d requests, want 1", len(urls))
	}
	if !strings.Contains(urls[0], "/groups?") {
		t.Errorf("url = %q, want it to contain /groups?", urls[0])
	}
}

func TestLiteralPrincipalIdSkipsDirectoryLookupWithoutAType(t *testing.T) {
	var urls []string
	provider := NewAzureProviderWith(syntheticCli{}, &syntheticTransport{urls: &urls}, "", "")
	principal := "00000000-0000-0000-0000-000000000003"

	id, err := provider.ResolvePrincipal(principal, "")
	if err != nil {
		t.Fatalf("ResolvePrincipal: %v", err)
	}
	if id != principal {
		t.Errorf("id = %q, want %q", id, principal)
	}
	if len(urls) != 0 {
		t.Errorf("expected no requests, got %v", urls)
	}
}

// TestOriginEncodingIsStrict pins percentEncode's exact test vector — AT13.
func TestOriginEncodingIsStrict(t *testing.T) {
	got := percentEncode("a b/'")
	want := "a%20b%2F%27"
	if got != want {
		t.Errorf("percentEncode(%q) = %q, want %q", "a b/'", got, want)
	}
}

// TestPercentEncodeDivergesFromStdlibURLEscape confirms net/url's escapers
// don't accidentally match percentEncode's strict unreserved-only set —
// QueryEscape encodes space as "+" (not "%20"), and PathEscape leaves "@"
// unescaped (a realistic character in a UPN-style principal name) — AT13.
func TestPercentEncodeDivergesFromStdlibURLEscape(t *testing.T) {
	spaceInput := "a b/'"
	if url.QueryEscape(spaceInput) == percentEncode(spaceInput) {
		t.Errorf("percentEncode should diverge from url.QueryEscape on %q", spaceInput)
	}
	atInput := "someone@example.com"
	if url.PathEscape(atInput) == percentEncode(atInput) {
		t.Errorf("percentEncode should diverge from url.PathEscape on %q", atInput)
	}
}

// TestRoleIdsAreStable pins deterministicUUID against independently
// computed RFC4122 UUIDv5 golden values (Python's uuid.uuid5(NAMESPACE_URL,
// ...), which implements the identical standard algorithm) — AT10. Role
// assignments/definitions rkit's attune already created live in tenants
// must resolve to the same resource ID here, or apply would create
// duplicates instead of updating them.
func TestRoleIdsAreStable(t *testing.T) {
	first := deterministicUUID("same")
	second := deterministicUUID("same")
	if first != second {
		t.Errorf("deterministicUUID is not stable for repeated input: %q != %q", first, second)
	}
	cases := map[string]string{
		"same":        "aea2349f-e45c-58dc-878c-a0c479952c2d",
		"role|reader": "924f49f0-8e6e-55c5-a29e-67daf0afe5d7",
	}
	for input, want := range cases {
		if got := deterministicUUID(input); got != want {
			t.Errorf("deterministicUUID(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestArmScopesRoundTripIntoTypedFields(t *testing.T) {
	scope := parseArmScope("/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/example-resources/providers/Microsoft.Network/dnsZones/example.com")
	if scope.ResourceGroup != "example-resources" {
		t.Errorf("ResourceGroup = %q, want %q", scope.ResourceGroup, "example-resources")
	}
	if scope.DnsZone != "example.com" {
		t.Errorf("DnsZone = %q, want %q", scope.DnsZone, "example.com")
	}
	armID, err := scope.ArmID("")
	if err != nil {
		t.Fatalf("ArmID: %v", err)
	}
	want := "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/example-resources/providers/Microsoft.Network/dnsZones/example.com"
	if armID != want {
		t.Errorf("ArmID = %q, want %q", armID, want)
	}
}

// TestSensitiveErrorsAreRedacted pins sanitizeBody's exact behavior — AT12.
func TestSensitiveErrorsAreRedacted(t *testing.T) {
	got := sanitizeBody([]byte(`{"access_token":"secret"}`))
	want := "provider response was redacted"
	if got != want {
		t.Errorf("sanitizeBody = %q, want %q", got, want)
	}
}

// TestNon2xxResponseRedactsSensitiveBody verifies the full request()
// pipeline never lets a raw provider response body — including credential
// fragments — reach the returned error, not just sanitizeBody in
// isolation — AT12.
func TestNon2xxResponseRedactsSensitiveBody(t *testing.T) {
	var urls []string
	transport := &syntheticTransport{
		urls:      &urls,
		responses: []*HTTPResponse{{Status: 403, Body: []byte(`{"access_token":"super-secret-value"}`)}},
	}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	_, err := provider.request("GET", "https://graph.microsoft.com/v1.0/me", nil)
	if err == nil {
		t.Fatal("expected error for non-2xx response")
	}
	if strings.Contains(err.Error(), "super-secret-value") {
		t.Errorf("error leaked sensitive body content: %v", err)
	}
	if !strings.Contains(err.Error(), "redacted") {
		t.Errorf("error does not mention redaction: %v", err)
	}
}

// TestOriginAllowlistRejectsUnknownHosts confirms request() refuses to
// attach the bearer token to (or ever call the transport for) any URL
// outside the ARM/Graph origins — AT11.
func TestOriginAllowlistRejectsUnknownHosts(t *testing.T) {
	var urls []string
	provider := NewAzureProviderWith(syntheticCli{}, &syntheticTransport{urls: &urls}, "", "")
	_, err := provider.request("GET", "https://evil.example.com/data", nil)
	if err == nil {
		t.Fatal("expected error for unsupported origin")
	}
	if !strings.Contains(err.Error(), "unsupported origin") {
		t.Errorf("error = %v, want mention of unsupported origin", err)
	}
	if len(urls) != 0 {
		t.Errorf("transport was invoked for a disallowed origin: %v", urls)
	}
}

// TestPaginationFollowsNextLink confirms pages() follows both ARM's
// "nextLink" and Graph's "@odata.nextLink" until exhausted — AT16.
func TestPaginationFollowsNextLink(t *testing.T) {
	var urls []string
	transport := &syntheticTransport{
		urls: &urls,
		responses: []*HTTPResponse{
			{Status: 200, Body: []byte(`{"value":[{"id":"a"}],"nextLink":"https://management.azure.com/next"}`)},
			{Status: 200, Body: []byte(`{"value":[{"id":"b"}]}`)},
		},
	}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	items, err := provider.pages("https://management.azure.com/first")
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if len(urls) != 2 {
		t.Errorf("got %d requests, want 2 (one per page)", len(urls))
	}
}

// TestRequestConstructionIncludesBearerTokenAndAPIVersion verifies the
// actual HTTP request built by a live-command call: the Authorization
// bearer header, method, and DNS api-version selection — AT8.
func TestRequestConstructionIncludesBearerTokenAndAPIVersion(t *testing.T) {
	var urls []string
	var requests []*HTTPRequest
	provider := NewAzureProviderWith(syntheticCli{}, &syntheticTransport{urls: &urls, requests: &requests}, "sub-1", "rg-1")

	if err := provider.EnsureZone("example.com"); err != nil {
		t.Fatalf("EnsureZone: %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(requests))
	}
	req := requests[0]
	if req.Method != "PUT" {
		t.Errorf("Method = %q, want PUT", req.Method)
	}
	wantAuth := "Bearer synthetic-token"
	found := false
	for _, h := range req.Headers {
		if h[0] == "Authorization" {
			found = true
			if h[1] != wantAuth {
				t.Errorf("Authorization = %q, want %q", h[1], wantAuth)
			}
		}
	}
	if !found {
		t.Errorf("missing Authorization header in %+v", req.Headers)
	}
	if !strings.Contains(req.URL, "api-version=2018-05-01") {
		t.Errorf("URL missing DNS api-version: %q", req.URL)
	}
	if !strings.Contains(req.URL, "/dnsZones/example.com") {
		t.Errorf("URL missing dnsZones/example.com: %q", req.URL)
	}
}

func TestPaginationFollowsODataNextLink(t *testing.T) {
	var urls []string
	transport := &syntheticTransport{
		urls: &urls,
		responses: []*HTTPResponse{
			{Status: 200, Body: []byte(`{"value":[{"id":"a"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/next"}`)},
			{Status: 200, Body: []byte(`{"value":[{"id":"b"}]}`)},
		},
	}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	items, err := provider.pages("https://graph.microsoft.com/v1.0/first")
	if err != nil {
		t.Fatalf("pages: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
}

// TestHasZoneMapsStatusesWithoutLeakingBodies pins HasZone's status
// mapping: success means the zone exists, not-found means it does not,
// and any other status is a redacted error.
func TestHasZoneMapsStatusesWithoutLeakingBodies(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		exists  bool
		wantErr bool
	}{
		{"success", 200, `{"name":"example.com"}`, true, false},
		{"not found", 404, `{"error":{"code":"ResourceNotFound"}}`, false, false},
		{"other failure", 403, `{"access_token":"super-secret-value"}`, false, true},
	}
	for _, c := range cases {
		var urls []string
		transport := &syntheticTransport{
			urls:      &urls,
			responses: []*HTTPResponse{{Status: c.status, Body: []byte(c.body)}},
		}
		provider := NewAzureProviderWith(syntheticCli{}, transport, "sub", "rg")
		exists, err := provider.HasZone("example.com")
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error, got exists=%v", c.name, exists)
				continue
			}
			if strings.Contains(err.Error(), "super-secret-value") {
				t.Errorf("%s: error leaked response body: %v", c.name, err)
			}
			if !strings.Contains(err.Error(), "redacted") {
				t.Errorf("%s: error does not mention redaction: %v", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: HasZone: %v", c.name, err)
			continue
		}
		if exists != c.exists {
			t.Errorf("%s: exists = %v, want %v", c.name, exists, c.exists)
		}
	}
}

// tenantCli records the tenant each token request names and fails for a
// tenant it was told to refuse.
type tenantCli struct {
	tenants *[]string
	refuse  string
}

func (tenantCli) Account() (Account, error) { return Account{Tenant: "home"}, nil }
func (c tenantCli) Token(_, tenant string) (string, error) {
	*c.tenants = append(*c.tenants, tenant)
	if tenant != "" && tenant == c.refuse {
		return "", fmt.Errorf("run Azure CLI token lookup: exit 1; run `az login --tenant %s` and retry", tenant)
	}
	return "token-" + tenant, nil
}

const liveAppJSON = `{"value":[{"id":"app-object","appId":"app-client","signInAudience":"AzureADMultipleOrgs",
"web":{"redirectUris":["https://example.com/cb"],"implicitGrantSettings":{"enableIdTokenIssuance":true}},
"passwordCredentials":[{"keyId":"k1","displayName":"host","startDateTime":"2026-01-01T00:00:00Z","endDateTime":"2027-01-01T00:00:00Z"}]}]}`

// TestGetAppReadsSignInFieldsAndSecretNames — AT4: the lookup selects the
// sign-in fields and credentials, and returns their names and dates
// without any value.
func TestGetAppReadsSignInFieldsAndSecretNames(t *testing.T) {
	var urls []string
	transport := &syntheticTransport{urls: &urls, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(liveAppJSON)},
		{Status: 200, Body: []byte(`{"value":[]}`)},
	}}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	app, err := provider.GetApp("", "site")
	if err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if !strings.Contains(urls[0], "$select=id,appId,signInAudience,web,passwordCredentials") {
		t.Errorf("lookup URL = %s", urls[0])
	}
	if app.SignInAudience != "AzureADMultipleOrgs" || len(app.RedirectURIs) != 1 || app.IDTokenIssuance == nil || !*app.IDTokenIssuance {
		t.Errorf("app = %+v", app)
	}
	if len(app.Secrets) != 1 || app.Secrets[0].Name != "host" || app.Secrets[0].KeyID != "k1" || app.Secrets[0].Expires.Year() != 2027 || app.Secrets[0].Issued.Year() != 2026 {
		t.Errorf("secrets = %+v", app.Secrets)
	}
}

// TestPutAppSendsSignInPayloadOnCreateAndUpdate — AT4: a create carries the
// managed fields in its body, an update PATCHes them, and an app with no
// managed field sends no PATCH.
func TestPutAppSendsSignInPayloadOnCreateAndUpdate(t *testing.T) {
	audience := "AzureADMyOrg"
	app := AppRegistration{Name: "site", SignInAudience: audience, RedirectURIs: []string{"https://example.com/cb"}, IDTokenIssuance: new(true)}
	bodyOf := func(req *HTTPRequest) map[string]any {
		var body map[string]any
		if err := json.Unmarshal(req.Body, &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		return body
	}
	checkPayload := func(body map[string]any) {
		web, _ := body["web"].(map[string]any)
		grant, _ := web["implicitGrantSettings"].(map[string]any)
		if body["signInAudience"] != audience || grant["enableIdTokenIssuance"] != true {
			t.Errorf("payload = %v", body)
		}
		if uris, _ := web["redirectUris"].([]any); len(uris) != 1 || uris[0] != "https://example.com/cb" {
			t.Errorf("redirectUris = %v", web["redirectUris"])
		}
	}

	var urls []string
	var requests []*HTTPRequest
	transport := &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(`{"value":[]}`)},
		{Status: 201, Body: []byte(`{"id":"app-object","appId":"app-client"}`)},
		{Status: 200, Body: []byte(`{"value":[]}`)},
	}}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	if err := provider.PutApp(app); err != nil {
		t.Fatalf("PutApp create: %v", err)
	}
	if requests[1].Method != "POST" || !strings.HasSuffix(requests[1].URL, "/applications") {
		t.Fatalf("second request = %s %s", requests[1].Method, requests[1].URL)
	}
	created := bodyOf(requests[1])
	if created["displayName"] != "site" {
		t.Errorf("create body = %v", created)
	}
	checkPayload(created)

	urls, requests = nil, nil
	transport = &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(`{"value":[{"id":"app-object","appId":"app-client"}]}`)},
		{Status: 204},
		{Status: 200, Body: []byte(`{"value":[]}`)},
	}}
	provider = NewAzureProviderWith(syntheticCli{}, transport, "", "")
	if err := provider.PutApp(app); err != nil {
		t.Fatalf("PutApp update: %v", err)
	}
	if requests[1].Method != "PATCH" || !strings.HasSuffix(requests[1].URL, "/applications/app-object") {
		t.Fatalf("second request = %s %s", requests[1].Method, requests[1].URL)
	}
	checkPayload(bodyOf(requests[1]))

	urls, requests = nil, nil
	transport = &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(`{"value":[{"id":"app-object","appId":"app-client"}]}`)},
		{Status: 200, Body: []byte(`{"value":[]}`)},
	}}
	provider = NewAzureProviderWith(syntheticCli{}, transport, "", "")
	if err := provider.PutApp(AppRegistration{Name: "site"}); err != nil {
		t.Fatalf("PutApp bare: %v", err)
	}
	for _, req := range requests {
		if req.Method == "PATCH" {
			t.Errorf("bare app sent a PATCH: %s", req.URL)
		}
	}
}

// TestAppSecretsAddAndRemoveThroughGraph — AT4: adding a secret posts
// addPassword with the display name and an expiry validForDays from now
// and returns the one-time value; removing posts removePassword with the
// key id.
func TestAppSecretsAddAndRemoveThroughGraph(t *testing.T) {
	restore := now
	now = func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	defer func() { now = restore }()
	var urls []string
	var requests []*HTTPRequest
	transport := &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(`{"value":[{"id":"app-object"}]}`)},
		{Status: 200, Body: []byte(`{"keyId":"k2","secretText":"one-time-value"}`)},
		{Status: 200, Body: []byte(`{"value":[{"id":"app-object"}]}`)},
		{Status: 204},
	}}
	provider := NewAzureProviderWith(syntheticCli{}, transport, "", "")
	value, err := provider.AddAppSecret(AppRegistration{Name: "site"}, Secret{Name: "host", ValidForDays: 30})
	if err != nil {
		t.Fatalf("AddAppSecret: %v", err)
	}
	if value != "one-time-value" {
		t.Errorf("value = %q", value)
	}
	if requests[1].Method != "POST" || !strings.HasSuffix(requests[1].URL, "/applications/app-object/addPassword") {
		t.Fatalf("add request = %s %s", requests[1].Method, requests[1].URL)
	}
	want := `{"passwordCredential":{"displayName":"host","endDateTime":"2026-11-26T12:00:00Z"}}`
	if string(requests[1].Body) != want {
		t.Errorf("add body = %s, want %s", requests[1].Body, want)
	}
	if err := provider.RemoveAppSecret(AppRegistration{Name: "site"}, "k1"); err != nil {
		t.Fatalf("RemoveAppSecret: %v", err)
	}
	if !strings.HasSuffix(requests[3].URL, "/applications/app-object/removePassword") || string(requests[3].Body) != `{"keyId":"k1"}` {
		t.Errorf("remove request = %s %s", requests[3].URL, requests[3].Body)
	}

	// A secret added right after PutApp created the registration reuses the
	// id from the create response instead of a display-name lookup.
	urls, requests = nil, nil
	transport = &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{
		{Status: 200, Body: []byte(`{"value":[]}`)},
		{Status: 201, Body: []byte(`{"id":"fresh-object","appId":"fresh-client"}`)},
		{Status: 200, Body: []byte(`{"value":[]}`)},
		{Status: 200, Body: []byte(`{"keyId":"k3","secretText":"fresh-value"}`)},
	}}
	provider = NewAzureProviderWith(syntheticCli{}, transport, "", "")
	if err := provider.PutApp(AppRegistration{Name: "fresh"}); err != nil {
		t.Fatalf("PutApp: %v", err)
	}
	if _, err := provider.AddAppSecret(AppRegistration{Name: "fresh"}, Secret{Name: "host", ValidForDays: 30}); err != nil {
		t.Fatalf("AddAppSecret after create: %v", err)
	}
	last := requests[len(requests)-1]
	if !strings.HasSuffix(last.URL, "/applications/fresh-object/addPassword") {
		t.Errorf("add after create = %s", last.URL)
	}
	for _, u := range urls[3:] {
		if strings.Contains(u, "displayName%20eq") || strings.Contains(u, "displayName eq") {
			t.Errorf("add after create looked the app up by name: %s", u)
		}
	}
}

// TestAppCallsInAnotherTenantUseThatTenantsToken — AT5: a lookup in another
// tenant requests a Graph token for it, a refused tenant fails with the
// az login guidance before any request, and the signed-in tenant's calls
// keep their own token.
func TestAppCallsInAnotherTenantUseThatTenantsToken(t *testing.T) {
	other := "22222222-2222-2222-2222-222222222222"
	var tenants []string
	var urls []string
	var requests []*HTTPRequest
	transport := &syntheticTransport{urls: &urls, requests: &requests, responses: []*HTTPResponse{{Status: 200, Body: []byte(`{"value":[]}`)}}}
	provider := NewAzureProviderWith(tenantCli{tenants: &tenants}, transport, "", "")
	if _, err := provider.GetApp(other, "consumer"); err != nil {
		t.Fatalf("GetApp: %v", err)
	}
	if _, err := provider.GetApp("", "home"); err != nil {
		t.Fatalf("GetApp home: %v", err)
	}
	if !slices.Equal(tenants, []string{other, ""}) {
		t.Errorf("token tenants = %v", tenants)
	}
	auth := func(i int) string {
		for _, h := range requests[i].Headers {
			if h[0] == "Authorization" {
				return h[1]
			}
		}
		return ""
	}
	if auth(0) != "Bearer token-"+other || auth(1) != "Bearer token-" {
		t.Errorf("authorization = %q, %q", auth(0), auth(1))
	}

	urls, requests, tenants = nil, nil, nil
	provider = NewAzureProviderWith(tenantCli{tenants: &tenants, refuse: other}, &syntheticTransport{urls: &urls, requests: &requests}, "", "")
	_, err := provider.ListAppNames(other)
	if err == nil || !strings.Contains(err.Error(), "az login --tenant "+other) {
		t.Errorf("refused tenant: got %v", err)
	}
	if len(requests) != 0 {
		t.Errorf("a request was sent without a token: %v", urls)
	}
}

// TestArmNotFoundNamesTheTarget: a zone check against a missing
// subscription or resource group fails with a message naming it instead of
// planning a new zone, a plain missing zone still reads as absent, and any
// other ARM 404 carries the subscription and resource group.
func TestArmNotFoundNamesTheTarget(t *testing.T) {
	respond := func(code string) *AzureProvider {
		var urls []string
		body := `{"error":{"code":"` + code + `","message":"tenant detail"}}`
		return NewAzureProviderWith(syntheticCli{}, &syntheticTransport{urls: &urls, responses: []*HTTPResponse{{Status: 404, Body: []byte(body)}}}, "sub02", "rg-1")
	}
	if _, err := respond("SubscriptionNotFound").HasZone("example.com"); err == nil || !strings.Contains(err.Error(), "subscription sub02 was not found") {
		t.Errorf("missing subscription: got %v", err)
	}
	if _, err := respond("ResourceGroupNotFound").HasZone("example.com"); err == nil || !strings.Contains(err.Error(), "resource group rg-1 was not found in subscription sub02") {
		t.Errorf("missing resource group: got %v", err)
	}
	exists, err := respond("ResourceNotFound").HasZone("example.com")
	if err != nil || exists {
		t.Errorf("missing zone: exists=%t err=%v", exists, err)
	}
	_, err = respond("SomethingElse").ListDNS("example.com")
	if err == nil || !strings.Contains(err.Error(), "subscription=sub02 resource-group=rg-1") || strings.Contains(err.Error(), "tenant detail") {
		t.Errorf("other 404: got %v", err)
	}
}
