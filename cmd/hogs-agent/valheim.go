package main

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var valheimConnectionsPattern = regexp.MustCompile(`\bConnections (\d+)\b`)

// valheimPlayerStatus derives occupancy from the server journal. Private
// servers do not service Steam queries, so A2S player counts are
// unavailable; the server does log a periodic "Connections N ..." line.
// Absent lines keep the count unknown rather than reporting a wrong zero.
func valheimPlayerStatus(server *ServerConfig) (players, maxPlayers int, known bool) {
	if strings.TrimSpace(server.Unit) == "" {
		return 0, 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "journalctl", "-u", server.Unit,
		"--since", "30 minutes ago", "--no-hostname", "-o", "cat").Output()
	if err != nil {
		return 0, 0, false
	}
	count, ok := parseValheimConnections(string(output))
	if !ok {
		return 0, 0, false
	}
	return count, 0, true
}

func parseValheimConnections(output string) (int, bool) {
	count := 0
	found := false
	for _, line := range strings.Split(output, "\n") {
		match := valheimConnectionsPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		parsed, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		count, found = parsed, true
	}
	return count, found
}
