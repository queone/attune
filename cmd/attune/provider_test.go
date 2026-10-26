package main

import (
	"slices"
	"strings"
	"testing"
)

// registerDNSOnlyProvider registers a DNS-only test provider named dnsonly
// with a dnsonly attune.yaml block, restoring the built-in registry when
// the test ends.
func registerDNSOnlyProvider(t *testing.T) {
	t.Helper()
	snapshot := snapshotRegistry()
	t.Cleanup(func() { restoreRegistry(snapshot) })
	Register(ProviderSpec{
		Name:        "dnsonly",
		Kinds:       []string{"dnsRecordSet"},
		ConfigBlock: "dnsonly",
		New: func(Settings, map[string]any) (LiveProvider, error) {
			return &dnsOnlyProvider{dns: fakeProvider{name: "dnsonly"}}, nil
		},
	})
}

func TestLookupProviderListsRegisteredNames(t *testing.T) {
	spec, err := LookupProvider("azure")
	if err != nil || spec.Name != "azure" {
		t.Fatalf("LookupProvider(azure) = %+v, %v", spec, err)
	}
	_, err = LookupProvider("nope")
	if err == nil || err.Error() != `unknown provider "nope"; registered: aws, azure` {
		t.Errorf("LookupProvider(nope) = %v", err)
	}
	registerDNSOnlyProvider(t)
	_, err = LookupProvider("nope")
	if err == nil || !strings.HasSuffix(err.Error(), "registered: aws, azure, dnsonly") {
		t.Errorf("LookupProvider(nope) with two providers = %v", err)
	}
}

func TestAzureRegistersEveryKindAndItsConfigBlock(t *testing.T) {
	spec, err := LookupProvider("azure")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.Kinds, azureKinds) || len(azureKinds) != 6 {
		t.Errorf("azure kinds = %v, want the six Azure kinds", spec.Kinds)
	}
	for _, kind := range validKinds {
		if _, err := LookupProvider("azure"); err == nil && !slices.Contains(azureKinds, kind) && checkKindSupport("azure", kind) == nil {
			t.Errorf("azure accepts %s, which it does not reconcile", kind)
		}
	}
	if spec.ConfigBlock != "azure" {
		t.Errorf("azure config block = %q, want azure", spec.ConfigBlock)
	}
	provider, err := spec.New(Settings{Subscription: "sub", ResourceGroup: "rg"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	azure, ok := provider.(*AzureProvider)
	if !ok || azure.Subscription != "sub" {
		t.Errorf("azure constructor returned %T %+v", provider, provider)
	}
	for _, kind := range azureKinds {
		if err := checkKindSupport("azure", kind); err != nil {
			t.Errorf("checkKindSupport(azure, %s) = %v", kind, err)
		}
	}
}

func TestRegistryResetRestoresAzureOnly(t *testing.T) {
	t.Run("registers a test provider", func(t *testing.T) {
		registerDNSOnlyProvider(t)
		if got := RegisteredProviders(); !slices.Equal(got, []string{"aws", "azure", "dnsonly"}) {
			t.Errorf("RegisteredProviders = %v", got)
		}
		if got := registeredConfigBlocks(); !slices.Equal(got, []string{"aws", "azure", "dnsonly"}) {
			t.Errorf("registeredConfigBlocks = %v", got)
		}
		if err := checkKindSupport("dnsonly", "securityGroup"); err == nil || err.Error() != `provider "dnsonly" does not support kind "securityGroup"` {
			t.Errorf("checkKindSupport(dnsonly, securityGroup) = %v", err)
		}
	})
	if got := RegisteredProviders(); !slices.Equal(got, []string{"aws", "azure"}) {
		t.Errorf("after cleanup RegisteredProviders = %v, want [aws azure]", got)
	}
}

// TestAwsRejectsIdentityKinds — aws registers DNS and the host kinds, so an
// identity kind under it fails offline.
func TestAwsRejectsIdentityKinds(t *testing.T) {
	if err := checkKindSupport("aws", "dnsRecordSet"); err != nil {
		t.Errorf("checkKindSupport(aws, dnsRecordSet) = %v", err)
	}
	if err := checkKindSupport("aws", "securityGroup"); err == nil || err.Error() != `provider "aws" does not support kind "securityGroup"` {
		t.Errorf("checkKindSupport(aws, securityGroup) = %v", err)
	}
	_, err := loadSources([]specSource{{name: "group.yaml", content: []byte("provider: aws\nkind: securityGroup\nname: eng\n")}}, "azure")
	if err == nil || err.Error() != `provider "aws" does not support kind "securityGroup"` {
		t.Errorf("securityGroup under aws: %v", err)
	}
}
