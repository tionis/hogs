// Package gametypes defines the optional game-specific behavior used by HOGS.
//
// Generic game types deliberately have no hooks. Adding an embedded game means
// registering one Driver; callers must resolve the persisted kind and enabled
// state before using its hooks.
package gametypes

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/tionis/hogs/backend"
)

const (
	KindEmbedded = "embedded"
	KindGeneric  = "generic"
)

type PlayerStatusParser func(string) (players, maxPlayers int, known bool)
type IdentityValidator func(string) bool
type IdentityResolver func(context.Context, *http.Client, string) (ResolvedIdentity, error)
type PlayerCommand func(string) string
type ConsoleLineFilter func(string) bool
type WhitelistListParser func(string) []string

var ErrExternalIdentityRequired = errors.New("verified external identity required")

type ResolvedIdentity struct {
	Username   string
	ExternalID string
}

type DetailField struct {
	Key   string
	Label string
}

type WhitelistDriver struct {
	Commands *CommandWhitelistDriver
	File     *FileWhitelistDriver
}

type CommandWhitelistDriver struct {
	ListCommand   string
	AddCommand    PlayerCommand
	RemoveCommand PlayerCommand
	ParseList     WhitelistListParser
}

type FileWhitelistDriver struct {
	Path                   string
	AllowReadWhileRunning  bool
	AllowWriteWhileRunning bool
	ChangesRequireRestart  bool
	Decode                 func([]byte) ([]backend.WhitelistEntry, error)
	Encode                 func([]backend.WhitelistEntry) ([]byte, error)
	BuildEntry             func(username, externalID string, readRelative func(string) ([]byte, error)) (backend.WhitelistEntry, error)
}

type Driver struct {
	Slug           string
	Kind           string
	DisplayName    string
	PlayerNoun     string
	Icon           string
	AccentColor    string
	StatusProtocol string
	Details        []DetailField

	PlayerStatusCommand   string
	ParsePlayerStatus     PlayerStatusParser
	VersionCommand        string
	ValidateIdentity      IdentityValidator
	ResolveIdentity       IdentityResolver
	IdentityCaseSensitive bool
	IdentityLabel         string
	// IdentityProvider is the key in Authentik's game_identities user
	// attribute. IdentityFromProvider converts that provider record into the
	// identifier expected by the game.
	IdentityProvider     string
	IdentityFromProvider func(username, subject string) ResolvedIdentity
	Whitelist            *WhitelistDriver
	IsRoutineConsoleLine ConsoleLineFilter
}

func (d Driver) AuthentikIdentity(username, subject string) (ResolvedIdentity, bool) {
	if d.IdentityProvider == "" {
		return ResolvedIdentity{}, false
	}
	resolved := ResolvedIdentity{Username: username, ExternalID: subject}
	if d.IdentityFromProvider != nil {
		resolved = d.IdentityFromProvider(username, subject)
	}
	return resolved, d.IdentityValid(resolved.Username)
}

var embedded = map[string]Driver{}

func Register(driver Driver) {
	if driver.Slug == "" {
		panic("gametypes: driver slug is required")
	}
	driver.Kind = KindEmbedded
	embedded[driver.Slug] = driver
}

func Embedded(slug string) (Driver, bool) {
	driver, ok := embedded[slug]
	return driver, ok
}

func AllEmbedded() []Driver {
	result := make([]Driver, 0, len(embedded))
	for _, driver := range embedded {
		result = append(result, driver)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slug < result[j].Slug })
	return result
}

func Generic(slug string) Driver {
	displayName := strings.TrimSpace(slug)
	if displayName == "" {
		displayName = "Generic"
	}
	return Driver{
		Slug: slug, Kind: KindGeneric, DisplayName: displayName, PlayerNoun: "Players",
		AccentColor: "#666666",
	}
}

// Resolve returns embedded behavior only when the persisted record explicitly
// enables an embedded driver. Unknown drivers and disabled types are generic.
func Resolve(slug, kind string, enabled bool) Driver {
	if enabled && kind == KindEmbedded {
		if driver, ok := Embedded(slug); ok {
			return driver
		}
	}
	return Generic(slug)
}

func (d Driver) SupportsWhitelist() bool {
	return d.SupportsCommandWhitelist() || d.SupportsFileWhitelist()
}

func (d Driver) SupportsCommandWhitelist() bool {
	return d.Whitelist != nil && d.Whitelist.Commands != nil &&
		d.Whitelist.Commands.ListCommand != "" &&
		d.Whitelist.Commands.AddCommand != nil &&
		d.Whitelist.Commands.RemoveCommand != nil
}

func (d Driver) SupportsFileWhitelist() bool {
	return d.Whitelist != nil && d.Whitelist.File != nil &&
		d.Whitelist.File.Path != "" &&
		d.Whitelist.File.Decode != nil &&
		d.Whitelist.File.Encode != nil
}

func (d Driver) IdentityValid(username string) bool {
	if d.ValidateIdentity == nil {
		return username != ""
	}
	return d.ValidateIdentity(username)
}

func (d Driver) IdentitiesEqual(first, second string) bool {
	if d.IdentityCaseSensitive {
		return first == second
	}
	return strings.EqualFold(first, second)
}

func (d Driver) IdentityFieldLabel() string {
	if d.IdentityLabel != "" {
		return d.IdentityLabel
	}
	return "In-game username"
}
