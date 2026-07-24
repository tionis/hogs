package gametypes

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

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

func TestMinecraftIdentityResolverValidatesOfficialProfile(t *testing.T) {
	driver, _ := Embedded("minecraft")
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host != "api.minecraftservices.com" || !strings.HasSuffix(request.URL.Path, "/TestPlayer") {
			t.Fatalf("unexpected profile request: %s", request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Body:   io.NopCloser(strings.NewReader(`{"id":"123456781234123412341234567890ab","name":"TestPlayer"}`)),
			Header: make(http.Header),
		}, nil
	})}
	resolved, err := driver.ResolveIdentity(context.Background(), client, "TestPlayer")
	if err != nil || resolved.Username != "TestPlayer" ||
		resolved.ExternalID != "123456781234123412341234567890ab" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
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
