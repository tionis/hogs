package gametypes

import "testing"

func TestResolveRequiresEnabledEmbeddedKind(t *testing.T) {
	enabled := Resolve("minecraft", KindEmbedded, true)
	if enabled.Kind != KindEmbedded || !enabled.SupportsWhitelist() || enabled.StatusProtocol != "minecraft" {
		t.Fatalf("enabled embedded driver was not resolved: %#v", enabled)
	}

	for _, test := range []struct {
		name    string
		kind    string
		enabled bool
	}{
		{"disabled", KindEmbedded, false},
		{"generic", KindGeneric, true},
		{"unknown kind", "plugin", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := Resolve("minecraft", test.kind, test.enabled)
			if driver.Kind != KindGeneric || driver.StatusProtocol != "" ||
				driver.SupportsWhitelist() || driver.ParsePlayerStatus != nil {
				t.Fatalf("unexpected specialized behavior: %#v", driver)
			}
		})
	}
}

func TestEmbeddedDriversHaveUniqueDefinitions(t *testing.T) {
	drivers := AllEmbedded()
	if len(drivers) < 5 {
		t.Fatalf("embedded drivers=%d, want at least 5", len(drivers))
	}
	seen := map[string]bool{}
	for _, driver := range drivers {
		if driver.Kind != KindEmbedded || driver.DisplayName == "" ||
			driver.PlayerNoun == "" || driver.AccentColor == "" {
			t.Fatalf("incomplete embedded driver: %#v", driver)
		}
		if seen[driver.Slug] {
			t.Fatalf("duplicate driver %q", driver.Slug)
		}
		seen[driver.Slug] = true
	}
}
