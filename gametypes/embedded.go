package gametypes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var minecraftPlayerCount = regexp.MustCompile(`(?i)there are\s+(\d+)\s+of a max of\s+(\d+)\s+players online`)
var minecraftUsername = regexp.MustCompile(`^[a-zA-Z0-9_]{3,16}$`)
var minecraftUUID = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)

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
		ValidateIdentity: minecraftUsername.MatchString,
		ResolveIdentity:  resolveMinecraftIdentity,
		Whitelist: &WhitelistDriver{
			ListCommand:   "whitelist list",
			AddCommand:    func(player string) string { return fmt.Sprintf("whitelist add %s", player) },
			RemoveCommand: func(player string) string { return fmt.Sprintf("whitelist remove %s", player) },
			OfflineFile:   "whitelist.json",
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
		AccentColor:    "#3e2723",
		Icon:           `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><path d="M8 1L3 5v2l2-1v4l-2 2v2h3v-2l1-1 1 1v2h3v-2l-2-2V6l2 1V5L8 1z"/></svg>`,
		StatusProtocol: "valheim",
	})
	Register(Driver{
		Slug: "starrupture", DisplayName: "Star Rupture", PlayerNoun: "Players",
		AccentColor: "#4a148c",
		Icon:        `<svg class="game-icon" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="8" r="2"/><path d="M8 0v3M8 13v3M0 8h3M13 8h3M2.5 2.5l2 2M11.5 11.5l2 2M13.5 2.5l-2 2M4.5 11.5l-2 2"/></svg>`,
	})
}
