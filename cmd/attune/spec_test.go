package main

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestFixtureLoadsEveryKind(t *testing.T) {
	bundle, err := loadDir(t, "testdata/specs")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if bundle.Len() != 6 {
		t.Errorf("bundle.Len() = %d, want 6", bundle.Len())
	}
}

func TestDirectoryObjectIdRequiresExactUuidShape(t *testing.T) {
	valid := []string{
		"00000000-0000-0000-0000-000000000001",
		"ABCDEF00-1234-5678-9ABC-DEF012345678",
	}
	for _, v := range valid {
		if !isDirectoryObjectID(v) {
			t.Errorf("isDirectoryObjectID(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"synthetic-principal",
		"000000000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-00000000000z",
	}
	for _, v := range invalid {
		if isDirectoryObjectID(v) {
			t.Errorf("isDirectoryObjectID(%q) = true, want false", v)
		}
	}
}

func TestLoadSourcesParsesNamedContents(t *testing.T) {
	entries, err := os.ReadDir("testdata/specs")
	if err != nil {
		t.Fatal(err)
	}
	var sources []specSource
	for _, e := range entries {
		content, err := os.ReadFile(filepath.Join("testdata/specs", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, specSource{name: e.Name(), content: content})
	}
	bundle, err := loadSources(sources, "azure")
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if bundle.Len() != 6 {
		t.Errorf("bundle.Len() = %d, want 6", bundle.Len())
	}
	dup := append(slices.Clone(sources), specSource{name: "dup/" + sources[0].name, content: sources[0].content})
	if _, err := loadSources(dup, "azure"); err == nil || !strings.Contains(err.Error(), "duplicate resource key") {
		t.Errorf("duplicate key: got %v", err)
	}
	if _, err := loadSources([]specSource{{name: "broken.yaml", content: []byte("kind: [")}}, "azure"); err == nil || !strings.Contains(err.Error(), "parse broken.yaml") {
		t.Errorf("parse error names the source: got %v", err)
	}
}

// loadDir parses every spec file under dir the way add DIR and validate do.
func loadDir(t *testing.T, dir string) (*Bundle, error) {
	t.Helper()
	var paths []string
	if err := collectSpecPaths(dir, &paths); err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var sources []specSource
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, specSource{name: p, content: b})
	}
	return loadSources(sources, "azure")
}

const dnsSpecYAML = "kind: dnsRecordSet\nzone: example.com\ntype: A\nname: www\nttl: 300\nvalues:\n  - 192.0.2.10\n"

// TestSpecProviderFieldSelectsRegisteredProvider — AT1: a spec naming
// azure loads tagged with it; a spec naming an unregistered provider fails
// with the registered list.
func TestSpecProviderFieldSelectsRegisteredProvider(t *testing.T) {
	bundle, err := loadSources([]specSource{{name: "dns.yaml", content: []byte("provider: azure\n" + dnsSpecYAML)}}, "azure")
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(bundle.Dns) != 1 || bundle.Dns[0].Provider != "azure" {
		t.Fatalf("bundle.Dns = %+v, want one azure record", bundle.Dns)
	}
	_, err = loadSources([]specSource{{name: "dns.yaml", content: []byte("provider: nope\n" + dnsSpecYAML)}}, "azure")
	if err == nil || err.Error() != `unknown provider "nope"; registered: aws, azure` {
		t.Fatalf("unknown provider: %v", err)
	}
}

// TestSpecWithoutProviderUsesDefault — AT2: the default provider fills a
// spec that omits the field, and an unknown default fails the same way.
func TestSpecWithoutProviderUsesDefault(t *testing.T) {
	bundle, err := loadSources([]specSource{{name: "dns.yaml", content: []byte(dnsSpecYAML)}}, "azure")
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if bundle.Dns[0].Provider != "azure" {
		t.Errorf("Provider = %q, want azure", bundle.Dns[0].Provider)
	}
	if _, err := loadSources([]specSource{{name: "dns.yaml", content: []byte(dnsSpecYAML)}}, "nope"); err == nil || !strings.Contains(err.Error(), `unknown provider "nope"`) {
		t.Errorf("unknown default provider: %v", err)
	}
}

// TestValidateRejectsKindTheProviderDoesNotSupport — AT3: a kind outside
// the provider's registered kinds fails offline, and a supported kind
// loads.
func TestValidateRejectsKindTheProviderDoesNotSupport(t *testing.T) {
	registerDNSOnlyProvider(t)
	group := "provider: dnsonly\nkind: securityGroup\nname: eng\n"
	_, err := loadSources([]specSource{{name: "group.yaml", content: []byte(group)}}, "azure")
	if err == nil || err.Error() != `provider "dnsonly" does not support kind "securityGroup"` {
		t.Fatalf("unsupported kind: %v", err)
	}
	bundle, err := loadSources([]specSource{{name: "dns.yaml", content: []byte("provider: dnsonly\n" + dnsSpecYAML)}}, "azure")
	if err != nil || bundle.Dns[0].Provider != "dnsonly" {
		t.Fatalf("supported kind: bundle=%+v err=%v", bundle, err)
	}
}

// TestDuplicateKeysAreScopedPerProvider — AT6: the same record set under
// two providers is two resources; twice under one provider is a duplicate.
func TestDuplicateKeysAreScopedPerProvider(t *testing.T) {
	registerDNSOnlyProvider(t)
	bundle, err := loadSources([]specSource{
		{name: "azure.yaml", content: []byte("provider: azure\n" + dnsSpecYAML)},
		{name: "dnsonly.yaml", content: []byte("provider: dnsonly\n" + dnsSpecYAML)},
	}, "azure")
	if err != nil {
		t.Fatalf("loadSources: %v", err)
	}
	if len(bundle.Dns) != 2 || !slices.Equal(bundle.Providers(), []string{"azure", "dnsonly"}) {
		t.Fatalf("bundle.Dns = %+v providers = %v", bundle.Dns, bundle.Providers())
	}
	if part := bundle.ForProvider("dnsonly"); part.Len() != 1 || part.Dns[0].Provider != "dnsonly" {
		t.Errorf("ForProvider(dnsonly) = %+v", part)
	}
	_, err = loadSources([]specSource{
		{name: "a.yaml", content: []byte("provider: azure\n" + dnsSpecYAML)},
		{name: "b.yaml", content: []byte(dnsSpecYAML)},
	}, "azure")
	if err == nil || !strings.Contains(err.Error(), "duplicate resource key") {
		t.Errorf("same provider twice: %v", err)
	}
}

// TestHostKindsValidateShapes — AT1: the host kinds parse with their
// defaults and reject a missing type, a rule without fromPort, an unknown
// encryption value, and a provider that lacks the kind.
func TestHostKindsValidateShapes(t *testing.T) {
	load := func(doc string) (*Bundle, error) {
		return loadSources([]specSource{{name: "spec.yaml", content: []byte(doc)}}, "azure")
	}
	bundle, err := load("provider: aws\nkind: instance\nname: h\ntype: t4g.small\nimage: ami-0example\nrootDisk:\n  size: 20\n")
	if err != nil {
		t.Fatalf("instance: %v", err)
	}
	if i := bundle.Instances[0]; !i.Imdsv2Required || i.RootDisk.Type != "gp3" || !i.RootDisk.Encrypted {
		t.Errorf("instance defaults = %+v", i)
	}
	bundle, err = load("provider: aws\nkind: bucket\nname: b\nregion: us-east-1\nencryption: aes256\n")
	if err != nil || bundle.Buckets[0].PublicAccessBlock != (PublicAccessBlock{true, true, true, true}) {
		t.Errorf("bucket defaults: %+v %v", bundle, err)
	}
	for name, doc := range map[string]string{
		"instance without type":  "provider: aws\nkind: instance\nname: h\nimage: ami-0example\nrootDisk:\n  size: 20\n",
		"rule without fromPort":  "provider: aws\nkind: firewall\nname: f\nrules:\n  - protocol: tcp\n    toPort: 22\n    sources: [192.0.2.0/24]\n",
		"unknown encryption":     "provider: aws\nkind: bucket\nname: b\nregion: us-east-1\nencryption: rot13\n",
		"instance without disk":  "provider: aws\nkind: instance\nname: h\ntype: t4g.small\nimage: ami-0example\n",
		"address without target": "provider: aws\nkind: staticAddress\nname: a\n",
	} {
		if _, err := load(doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err = load("provider: azure\nkind: instance\nname: h\ntype: t4g.small\nimage: ami-0example\nrootDisk:\n  size: 20\n")
	if err == nil || err.Error() != `provider "azure" does not support kind "instance"` {
		t.Errorf("instance under azure: %v", err)
	}
}

// TestReferencesValidateOffline — AT1: a well-formed reference to a spec on
// the same provider loads, and each malformed or dangling form fails with
// an error naming the value and the problem.
func TestReferencesValidateOffline(t *testing.T) {
	address := specSource{name: "address.yaml", content: []byte("provider: aws\nkind: staticAddress\nname: example-host\ninstance: example-host\n")}
	record := func(value string) specSource {
		return specSource{name: "dns.yaml", content: []byte("provider: aws\nkind: dnsRecordSet\nzone: example.com\ntype: A\nname: docs\nttl: 300\nvalues:\n  - '" + value + "'\n")}
	}
	bundle, err := loadSources([]specSource{address, record("${staticAddress:example-host:publicIp}")}, "azure")
	if err != nil || bundle.Dns[0].Values[0] != "${staticAddress:example-host:publicIp}" {
		t.Fatalf("valid reference: %+v %v", bundle, err)
	}
	cases := map[string]string{
		"${staticAddress:example-host}":   "not a reference of the form",
		"${bucket:x:name}":                `kind "bucket" has no readable field`,
		"${staticAddress:x:privateIp}":    `has no field "privateIp"`,
		"${staticAddress:other:publicIp}": "names no staticAddress spec",
		"v=spf1 ${x} -all":                "not a reference of the form",
	}
	for value, want := range cases {
		_, err := loadSources([]specSource{address, record(value)}, "azure")
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "example.com|A|docs") {
			t.Errorf("%s: err=%v, want it to contain %q and the record key", value, err, want)
		}
	}
	registerDNSOnlyProvider(t)
	other := specSource{name: "dns.yaml", content: []byte("provider: dnsonly\nkind: dnsRecordSet\nzone: example.com\ntype: A\nname: docs\nttl: 300\nvalues:\n  - '${staticAddress:example-host:publicIp}'\n")}
	if _, err := loadSources([]specSource{address, other}, "azure"); err == nil || !strings.Contains(err.Error(), `target is on provider "aws", not "dnsonly"`) {
		t.Errorf("cross-provider reference: %v", err)
	}
}

// TestMonitoringKindsValidateShapes — AT1: budgets and health checks parse
// with their defaults and reject the documented bad shapes.
func TestMonitoringKindsValidateShapes(t *testing.T) {
	load := func(doc string) (*Bundle, error) {
		return loadSources([]specSource{{name: "spec.yaml", content: []byte(doc)}}, "azure")
	}
	bundle, err := load("provider: aws\nkind: budget\nname: b\namount: 15\nalerts:\n  - threshold: 80\n    emails: [ops@example.com]\n")
	if err != nil || bundle.Budgets[0].Currency != "USD" || bundle.Budgets[0].Amount != "15" || bundle.Budgets[0].Alerts[0].Forecast {
		t.Fatalf("budget defaults: %+v %v", bundle, err)
	}
	bundle, err = load("provider: aws\nkind: healthCheck\nname: h\ntarget: www.example.com\nprotocol: HTTPS\nport: 443\nemails: [ops@example.com]\n")
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if c := bundle.HealthChecks[0]; c.Path != "/" || c.Interval != 30 || c.FailureThreshold != 3 || !c.Alarm {
		t.Errorf("health check defaults = %+v", c)
	}
	for name, doc := range map[string]string{
		"budget without alerts":    "provider: aws\nkind: budget\nname: b\namount: 15\n",
		"budget non-decimal":       "provider: aws\nkind: budget\nname: b\namount: fifteen\nalerts:\n  - threshold: 80\n    emails: [a@example.com]\n",
		"budget alert no emails":   "provider: aws\nkind: budget\nname: b\namount: 15\nalerts:\n  - threshold: 80\n    emails: []\n",
		"check ICMP":               "provider: aws\nkind: healthCheck\nname: h\ntarget: 192.0.2.1\nprotocol: ICMP\nport: 1\nemails: [a@example.com]\n",
		"check interval 20":        "provider: aws\nkind: healthCheck\nname: h\ntarget: 192.0.2.1\nprotocol: TCP\nport: 22\ninterval: 20\nemails: [a@example.com]\n",
		"check threshold 11":       "provider: aws\nkind: healthCheck\nname: h\ntarget: 192.0.2.1\nprotocol: TCP\nport: 22\nfailureThreshold: 11\nemails: [a@example.com]\n",
		"check no emails":          "provider: aws\nkind: healthCheck\nname: h\ntarget: 192.0.2.1\nprotocol: TCP\nport: 22\n",
		"check TCP with path":      "provider: aws\nkind: healthCheck\nname: h\ntarget: 192.0.2.1\nprotocol: TCP\nport: 22\npath: /\nemails: [a@example.com]\n",
		"check host with hostname": "provider: aws\nkind: healthCheck\nname: h\ntarget: www.example.com\nhost: example.com\nprotocol: HTTPS\nport: 443\nemails: [a@example.com]\n",
		"check dangling reference": "provider: aws\nkind: healthCheck\nname: h\ntarget: ${staticAddress:nope:publicIp}\nprotocol: HTTPS\nport: 443\nemails: [a@example.com]\n",
	} {
		if _, err := load(doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	address := specSource{name: "address.yaml", content: []byte("provider: aws\nkind: staticAddress\nname: example-host\ninstance: example-host\n")}
	check := specSource{name: "check.yaml", content: []byte("provider: aws\nkind: healthCheck\nname: h\ntarget: ${staticAddress:example-host:publicIp}\nhost: example.com\nprotocol: HTTPS\nport: 443\nemails: [a@example.com]\n")}
	if _, err := loadSources([]specSource{address, check}, "azure"); err != nil {
		t.Errorf("referenced target: %v", err)
	}
}
