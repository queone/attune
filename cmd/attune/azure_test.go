package main

import (
	"net/url"
	"strings"
	"testing"
)

type syntheticCli struct{}

func (syntheticCli) Account() (Account, error)    { return Account{}, nil }
func (syntheticCli) Token(string) (string, error) { return "synthetic-token", nil }

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
