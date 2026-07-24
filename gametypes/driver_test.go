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

func TestMinecraftWhitelistListParser(t *testing.T) {
	driver, _ := Embedded("minecraft")
	got := driver.Whitelist.Commands.ParseList("There are 3 whitelisted player(s): Alex, Builder_42, Steve")
	want := []string{"Alex", "Builder_42", "Steve"}
	if len(got) != len(want) {
		t.Fatalf("parsed whitelist=%#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed whitelist=%#v, want %#v", got, want)
		}
	}
	if got := driver.Whitelist.Commands.ParseList("There are no whitelisted players"); len(got) != 0 {
		t.Fatalf("empty whitelist parsed as %#v", got)
	}
}

func TestFactorioWhitelistDriver(t *testing.T) {
	driver, ok := Embedded("factorio")
	if !ok || !driver.SupportsWhitelist() {
		t.Fatal("Factorio whitelist driver is not available")
	}
	for _, username := range []string{"Engineer_42", "Space Cadet", "player.name-1"} {
		if !driver.IdentityValid(username) {
			t.Errorf("valid Factorio username %q was rejected", username)
		}
	}
	for _, username := range []string{"", " leading", "trailing ", "line\nbreak", "slash/name", strings.Repeat("a", 61)} {
		if driver.IdentityValid(username) {
			t.Errorf("invalid Factorio username %q was accepted", username)
		}
	}
	if driver.Whitelist.Commands.ListCommand != "/whitelist get" ||
		driver.Whitelist.Commands.AddCommand("Engineer_42") != "/whitelist add Engineer_42" ||
		driver.Whitelist.Commands.RemoveCommand("Engineer_42") != "/whitelist remove Engineer_42" {
		t.Fatalf("unexpected Factorio whitelist commands: %#v", driver.Whitelist)
	}
	got := driver.Whitelist.Commands.ParseList("Whitelisted players: Alice, Bob")
	if len(got) != 2 || got[0] != "Alice" || got[1] != "Bob" {
		t.Fatalf("parsed whitelist=%#v", got)
	}
	if got := driver.Whitelist.Commands.ParseList("The whitelist is empty."); len(got) != 0 {
		t.Fatalf("empty whitelist parsed as %#v", got)
	}
}

func TestFactorioOfflineWhitelistCodec(t *testing.T) {
	driver, _ := Embedded("factorio")
	fileBackend := driver.Whitelist.File
	entries, err := fileBackend.Decode([]byte(`["Alice","Space Cadet"]`))
	if err != nil || len(entries) != 2 || entries[0].Name != "Alice" ||
		entries[1].Name != "Space Cadet" || entries[0].UUID != "" {
		t.Fatalf("decoded entries=%#v err=%v", entries, err)
	}
	encoded, err := fileBackend.Encode(entries)
	if err != nil || string(encoded) != "[\n  \"Alice\",\n  \"Space Cadet\"\n]\n" {
		t.Fatalf("encoded whitelist=%q err=%v", encoded, err)
	}
	entry, err := fileBackend.BuildEntry("Engineer_42", "", nil)
	if err != nil || entry.Name != "Engineer_42" || entry.UUID != "" {
		t.Fatalf("built entry=%#v err=%v", entry, err)
	}
	if _, err := fileBackend.Decode([]byte(`["valid","invalid/name"]`)); err == nil {
		t.Fatal("invalid Factorio whitelist entry was accepted")
	}
}

func TestValheimFileBackedWhitelistDriver(t *testing.T) {
	driver, ok := Embedded("valheim")
	if !ok || !driver.SupportsWhitelist() || driver.SupportsCommandWhitelist() ||
		!driver.SupportsFileWhitelist() || !driver.Whitelist.File.AllowReadWhileRunning ||
		!driver.Whitelist.File.AllowWriteWhileRunning || !driver.Whitelist.File.ChangesRequireRestart {
		t.Fatalf("unexpected Valheim whitelist driver: %#v", driver)
	}
	for _, identity := range []string{
		"Steam_76561198000000000",
		"Xbox_2533274800000000",
		"PlayFab_ABCDEF0123456789",
	} {
		if !driver.IdentityValid(identity) {
			t.Errorf("valid Valheim platform ID %q was rejected", identity)
		}
	}
	for _, identity := range []string{
		"", "Steam_", "_1234", "Steam_123 456", " Steam_123", "Steam_123\n",
	} {
		if driver.IdentityValid(identity) {
			t.Errorf("invalid Valheim platform ID %q was accepted", identity)
		}
	}
	if driver.IdentitiesEqual("Steam_ABC", "steam_ABC") {
		t.Fatal("Valheim platform IDs were compared case-insensitively")
	}

	fileBackend := driver.Whitelist.File
	entries, err := fileBackend.Decode([]byte("// List permitted players ID ONE per line\r\nSteam_123\r\nXbox_456\r\nSteam_123\r\n"))
	if err != nil || len(entries) != 2 || entries[0].Name != "Steam_123" || entries[1].Name != "Xbox_456" {
		t.Fatalf("decoded permitted list=%#v err=%v", entries, err)
	}
	encoded, err := fileBackend.Encode(entries)
	if err != nil || string(encoded) != "// List permitted players ID ONE per line\nSteam_123\nXbox_456\n" {
		t.Fatalf("encoded permitted list=%q err=%v", encoded, err)
	}
	if _, err := fileBackend.Decode([]byte("Steam_123\nnot a platform id\n")); err == nil {
		t.Fatal("invalid Valheim permitted-list entry was accepted")
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
