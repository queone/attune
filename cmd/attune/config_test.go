package main

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultsAreSafe(t *testing.T) {
	settings := Resolve(nil, Overrides{})
	if !settings.PruneDNS {
		t.Errorf("PruneDNS = false, want true")
	}
	if settings.PruneIdentities {
		t.Errorf("PruneIdentities = true, want false")
	}
	if settings.Provider != "azure" {
		t.Errorf("Provider = %q, want %q", settings.Provider, "azure")
	}
}

func TestExpansionPreservesUnicodeText(t *testing.T) {
	got, err := expandEnvironment("specs: café\n")
	if err != nil {
		t.Fatalf("expandEnvironment: %v", err)
	}
	if got != "specs: café\n" {
		t.Errorf("expandEnvironment(unicode) = %q, want unchanged", got)
	}
}

func TestConfigEnvironmentExpansion(t *testing.T) {
	t.Setenv("ATTUNE_TEST_SUBSCRIPTION", "expanded-subscription-id")
	content := "provider: azure\nazure:\n  subscription: $ATTUNE_TEST_SUBSCRIPTION\n  resource_group: ${ATTUNE_TEST_SUBSCRIPTION}-rg\nspecs: specs\n"
	cfg, err := ParseConfig([]byte(content))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.Subscription != "expanded-subscription-id" {
		t.Errorf("Subscription = %q, want %q", cfg.Subscription, "expanded-subscription-id")
	}
	if cfg.ResourceGroup != "expanded-subscription-id-rg" {
		t.Errorf("ResourceGroup = %q, want %q", cfg.ResourceGroup, "expanded-subscription-id-rg")
	}
}

func TestResolvePrecedenceFlagOverEnvOverFileOverDefault(t *testing.T) {
	t.Setenv("ARM_SUBSCRIPTION_ID", "")
	t.Setenv("ARM_RESOURCE_GROUP", "")

	// No config, no env, no override: built-in default (empty subscription).
	settings := Resolve(nil, Overrides{})
	if settings.Subscription != "" {
		t.Fatalf("baseline Subscription = %q, want empty", settings.Subscription)
	}

	// Config sets it.
	cfg := &Config{Subscription: "from-config"}
	settings = Resolve(cfg, Overrides{})
	if settings.Subscription != "from-config" {
		t.Errorf("config-level Subscription = %q, want %q", settings.Subscription, "from-config")
	}

	// Environment overrides config.
	t.Setenv("ARM_SUBSCRIPTION_ID", "from-env")
	settings = Resolve(cfg, Overrides{})
	if settings.Subscription != "from-env" {
		t.Errorf("env-level Subscription = %q, want %q", settings.Subscription, "from-env")
	}

	// Flag override wins over both.
	flagValue := "from-flag"
	settings = Resolve(cfg, Overrides{Subscription: &flagValue})
	if settings.Subscription != "from-flag" {
		t.Errorf("flag-level Subscription = %q, want %q", settings.Subscription, "from-flag")
	}
}

func TestContentVersionParsesAndResolves(t *testing.T) {
	cfg, err := ParseConfig([]byte("provider: azure\nspecs: specs\ncontent_version: v1.2.3\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.ContentVersion != "v1.2.3" {
		t.Errorf("ContentVersion = %q, want %q", cfg.ContentVersion, "v1.2.3")
	}
	settings := Resolve(cfg, Overrides{})
	if settings.ContentVersion != "v1.2.3" {
		t.Errorf("resolved ContentVersion = %q, want %q", settings.ContentVersion, "v1.2.3")
	}
}

func TestContentVersionAbsentResolvesEmpty(t *testing.T) {
	settings := Resolve(&Config{}, Overrides{})
	if settings.ContentVersion != "" {
		t.Errorf("ContentVersion = %q, want empty", settings.ContentVersion)
	}
}

func TestUnknownKeyStillRejectedBesideContentVersion(t *testing.T) {
	_, err := ParseConfig([]byte("provider: azure\ncontent_version: v1.2.3\nbogus_key: nope\n"))
	if err == nil || !strings.Contains(err.Error(), `"bogus_key"`) {
		t.Errorf("ParseConfig error = %v, want unknown-field rejection naming bogus_key", err)
	}
}

func TestSpecsKeyIsAcceptedAndIgnored(t *testing.T) {
	cfg, err := ParseConfig([]byte("provider: azure\nspecs: anything/at/all\n"))
	if err != nil {
		t.Fatalf("ParseConfig with a specs key: %v", err)
	}
	if cfg.Provider != "azure" {
		t.Fatalf("parsed %+v", *cfg)
	}
}

// TestProviderBlocksFollowTheRegistry — AT11: a block named after a
// registered provider parses and is kept raw for it, an unregistered block
// is an unknown key, and the azure block still parses as before.
func TestProviderBlocksFollowTheRegistry(t *testing.T) {
	if _, err := ParseConfig([]byte("provider: azure\ndnsonly:\n  token: x\n")); err == nil || !strings.Contains(err.Error(), `"dnsonly"`) {
		t.Fatalf("unregistered block: %v", err)
	}
	registerDNSOnlyProvider(t)
	cfg, err := ParseConfig([]byte("provider: azure\ndnsonly:\n  token: x\nazure:\n  subscription: sub\n  resource_group: rg\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.ProviderBlocks["dnsonly"]["token"] != "x" {
		t.Errorf("dnsonly block = %v", cfg.ProviderBlocks["dnsonly"])
	}
	if cfg.Subscription != "sub" || cfg.ResourceGroup != "rg" || cfg.ProviderBlocks["azure"]["subscription"] != "sub" {
		t.Errorf("azure block: cfg=%+v", cfg)
	}
	if _, err := ParseConfig([]byte("provider: azure\ngcp:\n  region: x\n")); err == nil || !strings.Contains(err.Error(), `"gcp"`) {
		t.Errorf("still-unregistered block: %v", err)
	}
}

// TestPruneHostsParsesFromConfig — AT6: prune.hosts parses, resolves, and
// stays off when absent.
func TestPruneHostsParsesFromConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte("provider: aws\nprune:\n  hosts: true\n"))
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg.PruneHosts == nil || !*cfg.PruneHosts || !Resolve(cfg, Overrides{}).PruneHosts {
		t.Errorf("prune.hosts = %v", cfg.PruneHosts)
	}
	off := false
	if Resolve(cfg, Overrides{PruneHosts: &off}).PruneHosts {
		t.Error("flag false did not override config true")
	}
	if Resolve(&Config{}, Overrides{}).PruneHosts {
		t.Error("PruneHosts on without config")
	}
}

func TestPruneMonitoringParsesFromConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte("provider: aws\nprune:\n  monitoring: true\n"))
	if err != nil || cfg.PruneMonitoring == nil || !*cfg.PruneMonitoring || !Resolve(cfg, Overrides{}).PruneMonitoring {
		t.Errorf("prune.monitoring: cfg=%+v err=%v", cfg, err)
	}
	if Resolve(&Config{}, Overrides{}).PruneMonitoring {
		t.Error("PruneMonitoring on without config")
	}
}

// TestUnsetConfigVariableIsRefused: an unset ${VAR} in attune.yaml fails
// with a message naming it, a variable set to "" still expands, and a
// full-line comment is never expanded.
func TestUnsetConfigVariableIsRefused(t *testing.T) {
	os.Unsetenv("ATTUNE_TEST_UNSET")
	t.Cleanup(func() { os.Unsetenv("ATTUNE_TEST_UNSET") })
	_, err := ParseConfig([]byte("provider: azure\nazure:\n  subscription: ${ATTUNE_TEST_UNSET}\n"))
	if err == nil || !strings.Contains(err.Error(), "$ATTUNE_TEST_UNSET, which is not set") {
		t.Errorf("unset variable: got %v", err)
	}
	t.Setenv("ATTUNE_TEST_EMPTY", "")
	if _, err := ParseConfig([]byte("provider: azure\nazure:\n  resource_group: ${ATTUNE_TEST_EMPTY}\n")); err != nil {
		t.Errorf("empty variable: %v", err)
	}
	if _, err := ParseConfig([]byte("# uses $ATTUNE_TEST_UNSET when set\nprovider: azure\n")); err != nil {
		t.Errorf("comment line: %v", err)
	}
}
