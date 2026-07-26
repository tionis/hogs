package gametypes

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/tionis/hogs/backend"
)

var minecraftPlayerCount = regexp.MustCompile(`(?i)there are\s+(\d+)\s+of a max of\s+(\d+)\s+players online`)
var minecraftUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)
var minecraftUUID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
var factorioUsername = regexp.MustCompile(`^[a-zA-Z0-9_. -]{1,60}$`)
var valheimPlatformID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{0,31}_[A-Za-z0-9][A-Za-z0-9-]{0,127}$`)

const valheimPermittedListHeader = "// List permitted players ID ONE per line"

func encodeJSON(value interface{}) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func decodeMinecraftWhitelist(raw []byte) ([]backend.WhitelistEntry, error) {
	var entries []backend.WhitelistEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []backend.WhitelistEntry{}
	}
	return entries, nil
}

func minecraftWhitelistEntry(username, externalID string, readRelative func(string) ([]byte, error)) (backend.WhitelistEntry, error) {
	onlineMode := true
	if raw, err := readRelative("server.properties"); err == nil {
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "#") {
				continue
			}
			key, value, found := strings.Cut(line, "=")
			if found && strings.TrimSpace(key) == "online-mode" {
				onlineMode = !strings.EqualFold(strings.TrimSpace(value), "false")
				break
			}
		}
	}
	if !onlineMode {
		externalID = offlineMinecraftUUID(username)
	}
	formatted, err := formatMinecraftUUID(externalID)
	if err != nil {
		return backend.WhitelistEntry{}, ErrExternalIdentityRequired
	}
	return backend.WhitelistEntry{UUID: formatted, Name: username}, nil
}

func offlineMinecraftUUID(username string) string {
	sum := md5.Sum([]byte("OfflinePlayer:" + username))
	sum[6] = (sum[6] & 0x0f) | 0x30
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

func formatMinecraftUUID(value string) (string, error) {
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	if len(compact) != 32 {
		return "", fmt.Errorf("invalid Minecraft UUID")
	}
	for _, char := range compact {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return "", fmt.Errorf("invalid Minecraft UUID")
		}
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" +
		compact[16:20] + "-" + compact[20:32], nil
}

func validFactorioUsername(username string) bool {
	return username == strings.TrimSpace(username) && factorioUsername.MatchString(username)
}

func decodeFactorioWhitelist(raw []byte) ([]backend.WhitelistEntry, error) {
	var usernames []string
	if err := json.Unmarshal(raw, &usernames); err != nil {
		return nil, err
	}
	entries := make([]backend.WhitelistEntry, 0, len(usernames))
	for _, username := range usernames {
		if !validFactorioUsername(username) {
			return nil, fmt.Errorf("invalid Factorio username in whitelist")
		}
		entries = append(entries, backend.WhitelistEntry{Name: username})
	}
	return entries, nil
}

func encodeFactorioWhitelist(entries []backend.WhitelistEntry) ([]byte, error) {
	usernames := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validFactorioUsername(entry.Name) {
			return nil, fmt.Errorf("invalid Factorio username in whitelist")
		}
		usernames = append(usernames, entry.Name)
	}
	return encodeJSON(usernames)
}

func validValheimPlatformID(value string) bool {
	return value == strings.TrimSpace(value) && valheimPlatformID.MatchString(value)
}

func decodeValheimPermittedList(raw []byte) ([]backend.WhitelistEntry, error) {
	var entries []backend.WhitelistEntry
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if !validValheimPlatformID(line) {
			return nil, fmt.Errorf("invalid Valheim platform user ID in permitted list")
		}
		if !seen[line] {
			entries = append(entries, backend.WhitelistEntry{Name: line})
			seen[line] = true
		}
	}
	return entries, nil
}

func encodeValheimPermittedList(entries []backend.WhitelistEntry) ([]byte, error) {
	identities := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !validValheimPlatformID(entry.Name) {
			return nil, fmt.Errorf("invalid Valheim platform user ID in permitted list")
		}
		identities = append(identities, entry.Name)
	}
	lines := append([]string{valheimPermittedListHeader}, identities...)
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func resolveMinecraftIdentity(ctx context.Context, client *http.Client, username string) (ResolvedIdentity, error) {
	if !minecraftUsername.MatchString(username) {
		return ResolvedIdentity{}, fmt.Errorf("invalid Minecraft username")
	}
	if client == nil {
		client = http.DefaultClient
	}
	endpoint := "https://api.minecraftservices.com/minecraft/profile/lookup/name/" + url.PathEscape(username)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ResolvedIdentity{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return ResolvedIdentity{}, fmt.Errorf("resolve Minecraft profile: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNoContent {
		return ResolvedIdentity{}, fmt.Errorf("Minecraft account %q was not found", username)
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		return ResolvedIdentity{}, fmt.Errorf("Minecraft profile service returned %s", response.Status)
	}
	var profile struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&profile); err != nil {
		return ResolvedIdentity{}, fmt.Errorf("decode Minecraft profile: %w", err)
	}
	if !minecraftUsername.MatchString(profile.Name) || !minecraftUUID.MatchString(profile.ID) {
		return ResolvedIdentity{}, fmt.Errorf("Minecraft profile service returned an invalid profile")
	}
	return ResolvedIdentity{Username: profile.Name, ExternalID: strings.ToLower(profile.ID)}, nil
}

func init() {
	Register(Driver{
		Slug: "minecraft", DisplayName: "Minecraft", PlayerNoun: "Players",
		AccentColor:    "#2e7d32",
		Icon:           `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><rect x="4" y="4" width="3" height="3"/><rect x="9" y="4" width="3" height="3"/><rect x="4" y="9" width="3" height="3"/><rect x="9" y="9" width="3" height="3"/></svg>`,
		StatusProtocol: "minecraft", PlayerStatusCommand: "list",
		Details: []DetailField{{Key: "version", Label: "Server Version"}, {Key: "serverMessage", Label: "Server Message"}},
		ParsePlayerStatus: func(output string) (int, int, bool) {
			matches := minecraftPlayerCount.FindStringSubmatch(output)
			if len(matches) != 3 {
				return 0, 0, false
			}
			players, err := strconv.Atoi(matches[1])
			if err != nil {
				return 0, 0, false
			}
			maxPlayers, err := strconv.Atoi(matches[2])
			return players, maxPlayers, err == nil
		},
		ValidateIdentity:     minecraftUsername.MatchString,
		ResolveIdentity:      resolveMinecraftIdentity,
		IdentityLabel:        "Minecraft username",
		IdentityAccountLabel: "Minecraft",
		IdentityProvider:     "minecraft",
		Whitelist: &WhitelistDriver{
			Commands: &CommandWhitelistDriver{
				ListCommand:   "whitelist list",
				AddCommand:    func(player string) string { return fmt.Sprintf("whitelist add %s", player) },
				RemoveCommand: func(player string) string { return fmt.Sprintf("whitelist remove %s", player) },
				ParseList: func(output string) []string {
					_, players, found := strings.Cut(output, ":")
					if !found {
						return nil
					}
					var names []string
					for _, player := range strings.Split(players, ",") {
						if player = strings.TrimSpace(player); minecraftUsername.MatchString(player) {
							names = append(names, player)
						}
					}
					return names
				},
			},
			File: &FileWhitelistDriver{
				Path:       "whitelist.json",
				Decode:     decodeMinecraftWhitelist,
				Encode:     func(entries []backend.WhitelistEntry) ([]byte, error) { return encodeJSON(entries) },
				BuildEntry: minecraftWhitelistEntry,
			},
		},
		IsRoutineConsoleLine: func(line string) bool {
			return strings.Contains(line, "Thread RCON Client /") &&
				(strings.Contains(line, " started") || strings.Contains(line, " shutting down"))
		},
	})
	Register(Driver{
		Slug: "factorio", DisplayName: "Factorio", PlayerNoun: "Engineers",
		AccentColor:    "#827717",
		Icon:           `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><polygon points="8,1 15,8 8,15 1,8"/></svg>`,
		StatusProtocol: "factorio", PlayerStatusCommand: "/players", VersionCommand: "/version",
		Details: []DetailField{{Key: "version", Label: "Game Version"}},
		ParsePlayerStatus: func(output string) (players, maxPlayers int, known bool) {
			for _, line := range strings.Split(output, "\n") {
				if strings.HasSuffix(strings.TrimSpace(line), "(online)") {
					players++
				}
			}
			return players, 0, true
		},
		ValidateIdentity:     validFactorioUsername,
		IdentityLabel:        "Factorio username",
		IdentityAccountLabel: "Factorio",
		IdentityProvider:     "factorio",
		Whitelist: &WhitelistDriver{
			Commands: &CommandWhitelistDriver{
				ListCommand:   "/whitelist get",
				AddCommand:    func(player string) string { return fmt.Sprintf("/whitelist add %s", player) },
				RemoveCommand: func(player string) string { return fmt.Sprintf("/whitelist remove %s", player) },
				ParseList: func(output string) []string {
					for _, line := range strings.Split(output, "\n") {
						prefix, players, found := strings.Cut(strings.TrimSpace(line), ":")
						if !found || !strings.EqualFold(strings.TrimSpace(prefix), "Whitelisted players") {
							continue
						}
						var names []string
						for _, player := range strings.Split(players, ",") {
							if player = strings.TrimSpace(player); validFactorioUsername(player) {
								names = append(names, player)
							}
						}
						return names
					}
					return nil
				},
			},
			File: &FileWhitelistDriver{
				Path:   "factorio/server-whitelist.json",
				Decode: decodeFactorioWhitelist,
				Encode: encodeFactorioWhitelist,
				BuildEntry: func(username, _ string, _ func(string) ([]byte, error)) (backend.WhitelistEntry, error) {
					return backend.WhitelistEntry{Name: username}, nil
				},
			},
		},
		IsRoutineConsoleLine: func(line string) bool {
			return strings.Contains(line, "RemoteCommandProcessor.cpp:") &&
				strings.Contains(line, "New RCON connection from IP ADDR:")
		},
	})
	Register(Driver{
		Slug: "satisfactory", DisplayName: "Satisfactory", PlayerNoun: "Engineers",
		AccentColor:    "#e65100",
		Icon:           `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="8" r="3"/><circle cx="8" cy="2" r="1.5"/><circle cx="8" cy="14" r="1.5"/><circle cx="2" cy="8" r="1.5"/><circle cx="14" cy="8" r="1.5"/></svg>`,
		StatusProtocol: "satisfactory",
		Details:        []DetailField{{Key: "mapName", Label: "Active Session"}, {Key: "serverMessage", Label: "Server Name"}},
	})
	Register(Driver{
		Slug: "valheim", DisplayName: "Valheim", PlayerNoun: "Vikings",
		AccentColor:           "#3e2723",
		Icon:                  `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1L3 5v2l2-1v4l-2 2v2h3v-2l1-1 1 1v2h3v-2l-2-2V6l2 1V5L8 1z"/></svg>`,
		StatusProtocol:        "valheim",
		ValidateIdentity:      validValheimPlatformID,
		IdentityCaseSensitive: true,
		IdentityLabel:         "Platform User ID",
		IdentityAccountLabel:  "Steam",
		IdentityProvider:      "steam",
		IdentityFromProvider: func(_ string, subject string) ResolvedIdentity {
			return ResolvedIdentity{Username: "Steam_" + subject, ExternalID: subject}
		},
		Whitelist: &WhitelistDriver{
			File: &FileWhitelistDriver{
				Path:                   "permittedlist.txt",
				AllowReadWhileRunning:  true,
				AllowWriteWhileRunning: true,
				ChangesRequireRestart:  true,
				Decode:                 decodeValheimPermittedList,
				Encode:                 encodeValheimPermittedList,
				BuildEntry: func(platformID, _ string, _ func(string) ([]byte, error)) (backend.WhitelistEntry, error) {
					return backend.WhitelistEntry{Name: platformID}, nil
				},
			},
		},
	})
	Register(Driver{
		Slug: "starrupture", DisplayName: "Star Rupture", PlayerNoun: "Players",
		AccentColor: "#4a148c",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="8" r="2"/><path d="M8 0v3M8 13v3M0 8h3M13 8h3M2.5 2.5l2 2M11.5 11.5l2 2M13.5 2.5l-2 2M4.5 11.5l-2 2"/></svg>`,
	})
}
