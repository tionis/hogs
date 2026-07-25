package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func minecraftHandshakePacket(protocol, nextState int) []byte {
	body := appendMinecraftVarInt(nil, 0)
	body = appendMinecraftVarInt(body, protocol)
	body = append(body, minecraftString("game.example.test")...)
	var port [2]byte
	binary.BigEndian.PutUint16(port[:], 25565)
	body = append(body, port[:]...)
	body = appendMinecraftVarInt(body, nextState)
	return minecraftPacket(body)
}

func minecraftLoginPacket(username string) []byte {
	body := appendMinecraftVarInt(nil, 0)
	body = append(body, minecraftString(username)...)
	return minecraftPacket(body)
}

func offlineTestGateway(t *testing.T) *minecraftGateway {
	t.Helper()
	server := &ServerConfig{
		Unit: "minecraft-test.service", GameType: "minecraft",
		Gateway: GatewayConfig{
			Type: "minecraft", Listen: "127.0.0.1:25565", Backend: "127.0.0.1:25566",
		},
	}
	return &minecraftGateway{
		serverID: "test", server: server, config: server.Gateway, stateDir: t.TempDir(),
		state: "stopped", connectTimeout: time.Millisecond, readinessTimer: time.Second,
		wakeCooldown: time.Second, defaultBoot: 90 * time.Second,
		loginRateLimit: time.Second, loginAttempts: map[string]time.Time{},
		serviceStatus: func(string) (bool, string) { return false, "dead" },
		runAction: func(*ServerConfig, string) map[string]interface{} {
			return map[string]interface{}{"success": true}
		},
		dialTimeout: func(string, string, time.Duration) (net.Conn, error) {
			return nil, errors.New("offline")
		},
	}
}

func TestOfflineLoginRateLimitIsPerAddress(t *testing.T) {
	gateway := offlineTestGateway(t)
	first := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 1000}
	sameHost := &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 2000}
	otherHost := &net.TCPAddr{IP: net.ParseIP("192.0.2.11"), Port: 1000}
	if !gateway.allowOfflineLogin(first) {
		t.Fatal("first login was throttled")
	}
	if gateway.allowOfflineLogin(sameHost) {
		t.Fatal("second login from the same address was not throttled")
	}
	if !gateway.allowOfflineLogin(otherHost) {
		t.Fatal("unrelated address was throttled")
	}
}

func TestMinecraftPacketParsing(t *testing.T) {
	packet := minecraftHandshakePacket(770, 2)
	body, raw, err := readMinecraftPacket(bytes.NewReader(packet))
	if err != nil || !bytes.Equal(raw, packet) {
		t.Fatalf("read handshake err=%v raw=%x", err, raw)
	}
	handshake, err := parseMinecraftHandshake(body)
	if err != nil || handshake.Protocol != 770 || handshake.Host != "game.example.test" ||
		handshake.Port != 25565 || handshake.NextState != 2 {
		t.Fatalf("handshake=%#v err=%v", handshake, err)
	}
	loginBody, _, err := readMinecraftPacket(bytes.NewReader(minecraftLoginPacket("Test_Player")))
	username, parseErr := parseMinecraftLoginUsername(loginBody)
	if err != nil || parseErr != nil || username != "Test_Player" {
		t.Fatalf("username=%q readErr=%v parseErr=%v", username, err, parseErr)
	}
	if _, err := parseMinecraftLoginUsername(minecraftLoginPacket("invalid-name")); err == nil {
		t.Fatal("invalid username was accepted")
	}
}

func TestMinecraftStatusPingDoesNotWakeServer(t *testing.T) {
	gateway := offlineTestGateway(t)
	client, agent := net.Pipe()
	defer client.Close()
	go gateway.handleConnection(agent)
	if _, err := client.Write(minecraftHandshakePacket(770, 1)); err != nil {
		t.Fatal(err)
	}
	body, _, err := readMinecraftPacket(client)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(body)
	packetID, _, _ := readMinecraftVarInt(reader)
	statusJSON, err := readMinecraftString(reader, maxMinecraftPacket)
	if err != nil || packetID != 0 || !strings.Contains(statusJSON, "sleeps when empty") {
		t.Fatalf("status packet id=%d json=%q err=%v", packetID, statusJSON, err)
	}
	if gateway.wakeAttempts.Load() != 0 || gateway.wakeTriggered.Load() != 0 {
		t.Fatal("server-list ping triggered a wake")
	}
}

func TestMinecraftLoginTriggersOneWakeAndReturnsEstimate(t *testing.T) {
	gateway := offlineTestGateway(t)
	var ready atomic.Bool
	var actions atomic.Int32
	gateway.runAction = func(*ServerConfig, string) map[string]interface{} {
		actions.Add(1)
		ready.Store(true)
		return map[string]interface{}{"success": true}
	}
	gateway.dialTimeout = func(string, string, time.Duration) (net.Conn, error) {
		if !ready.Load() {
			return nil, errors.New("offline")
		}
		client, server := net.Pipe()
		go server.Close()
		return client, nil
	}

	client, agent := net.Pipe()
	defer client.Close()
	go gateway.handleConnection(agent)
	request := append(minecraftHandshakePacket(770, 2), minecraftLoginPacket("WakePlayer")...)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	body, _, err := readMinecraftPacket(client)
	if err != nil {
		t.Fatal(err)
	}
	reader := bytes.NewReader(body)
	packetID, _, _ := readMinecraftVarInt(reader)
	messageJSON, err := readMinecraftString(reader, maxMinecraftPacket)
	var message map[string]string
	_ = json.Unmarshal([]byte(messageJSON), &message)
	if err != nil || packetID != 0 || !strings.Contains(message["text"], "2 minute") {
		t.Fatalf("disconnect id=%d message=%q err=%v", packetID, messageJSON, err)
	}
	deadline := time.Now().Add(time.Second)
	for actions.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if actions.Load() != 1 || gateway.wakeAttempts.Load() != 1 || gateway.wakeTriggered.Load() != 1 {
		t.Fatalf("actions=%d attempts=%d triggered=%d", actions.Load(), gateway.wakeAttempts.Load(), gateway.wakeTriggered.Load())
	}
}

func TestGatewayEstimateUsesRecent75thPercentile(t *testing.T) {
	gateway := offlineTestGateway(t)
	gateway.bootDurations = []float64{40, 60, 80, 100}
	gateway.mu.Lock()
	estimate := gateway.estimatedBootLocked()
	gateway.mu.Unlock()
	if estimate != 80*time.Second {
		t.Fatalf("estimate=%s, want 80s", estimate)
	}
}

func TestGatewayBootHistoryPersistsInsideStateDirectory(t *testing.T) {
	gateway := offlineTestGateway(t)
	gateway.serverID = "../unsafe"
	gateway.bootDurations = []float64{42, 84}
	now := time.Now().UTC().Truncate(time.Second)
	gateway.lastReadyAt = &now
	gateway.saveState()

	if !strings.HasPrefix(gateway.statePath(), gateway.stateDir+string(filepath.Separator)) {
		t.Fatalf("state path escaped state directory: %s", gateway.statePath())
	}
	reloaded := offlineTestGateway(t)
	reloaded.serverID = gateway.serverID
	reloaded.stateDir = gateway.stateDir
	reloaded.loadState()
	if len(reloaded.bootDurations) != 2 || reloaded.bootDurations[1] != 84 ||
		reloaded.lastReadyAt == nil || !reloaded.lastReadyAt.Equal(now) {
		t.Fatalf("state did not round-trip: %#v", reloaded)
	}
}

func TestGatewayConfigurationRequiresMinecraftAndDistinctAddresses(t *testing.T) {
	server := ServerConfig{
		GameType: "minecraft",
		Gateway:  GatewayConfig{Type: "minecraft", Listen: ":25565", Backend: "127.0.0.1:25566"},
	}
	if err := validateGatewayConfig("cog", server); err != nil {
		t.Fatal(err)
	}
	server.Gateway.Backend = server.Gateway.Listen
	if err := validateGatewayConfig("cog", server); err == nil {
		t.Fatal("same listener and backend address was accepted")
	}
	server.GameType = "factorio"
	if err := validateGatewayConfig("cog", server); err == nil {
		t.Fatal("Minecraft gateway was accepted for another game type")
	}
}
