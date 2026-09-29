package main

import (
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
