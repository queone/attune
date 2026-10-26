package main

import "testing"

func TestScopeRequiresResourceGroupForDnsZone(t *testing.T) {
	scope := Scope{DnsZone: "example.com"}
	if _, err := scope.ArmID("00000000-0000-0000-0000-000000000001"); err == nil {
		t.Errorf("expected error for dnsZone scope without resourceGroup")
	}
}

func TestDnsValuesAreOrderInsensitive(t *testing.T) {
	left := DnsRecord{
		Zone:       "example.com",
		RecordType: "TXT",
		Name:       "@",
		TTL:        60,
		Values:     []string{"b", "a"},
	}
	right := left
	right.Values = []string{"a", "b"}
	if !left.SameData(right) {
		t.Errorf("expected order-insensitive DNS value comparison to match")
	}
}

func TestLocationNormalizationTreatsFormattingAsEqual(t *testing.T) {
	equal := [][2]string{
		{"East US", "eastus"},
		{"EastUS", "eastus"},
		{"eastus", "eastus"},
	}
	for _, pair := range equal {
		if !sameLocation(pair[0], pair[1]) {
			t.Errorf("sameLocation(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	if sameLocation("eastus", "westus2") {
		t.Error("sameLocation(eastus, westus2) = true, want false")
	}
}

func TestOutputsResolveLeavesPendingReferencesAsWritten(t *testing.T) {
	outputs := NewOutputs()
	outputs.Record("staticAddress", "a", "publicIp", "203.0.113.10")
	outputs.Record("instance", "i", "id", "")
	resolved, pending := outputs.Resolve([]string{"${staticAddress:a:publicIp}", "${instance:i:id}", "plain"})
	if resolved[0] != "203.0.113.10" || resolved[1] != "${instance:i:id}" || resolved[2] != "plain" {
		t.Errorf("resolved = %v", resolved)
	}
	if len(pending) != 1 || pending[0].String() != "${instance:i:id}" {
		t.Errorf("pending = %v", pending)
	}
	if _, ok, err := parseReference("192.0.2.1"); ok || err != nil {
		t.Errorf("plain value: ok=%v err=%v", ok, err)
	}
	if _, _, err := parseReference("${a:b}"); err == nil {
		t.Error("two-part reference accepted")
	}
}
