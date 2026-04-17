package query

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/tionis/hogs/database"
)

type FactorioQuerier struct{}

const (
	rconAuth         int32 = 3
	rconAuthResponse int32 = 2
	rconExec         int32 = 2
	rconResponse     int32 = 0
)

func (q *FactorioQuerier) Query(server *database.Server) (*ServerStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverStatus := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
	}

	password, hasPassword := server.Metadata["rcon_password"]
	rconAddr := server.Address
	if addr, ok := server.Metadata["rcon_address"]; ok && addr != "" {
		rconAddr = addr
	}
	if !hasPassword {
		conn, err := net.DialTimeout("tcp", rconAddr, 3*time.Second)
		if err != nil {
			serverStatus.Error = err.Error()
			return serverStatus, fmt.Errorf("failed to connect to factorio server: %w", err)
		}
		conn.Close()
		serverStatus.Online = true
		return serverStatus, nil
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", rconAddr)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to connect to factorio RCON: %w", err)
	}
	defer conn.Close()

	if err := rconSend(conn, 1, rconAuth, password); err != nil {
		serverStatus.Error = fmt.Sprintf("RCON auth send failed: %v", err)
		return serverStatus, err
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	resp, err := rconReceive(conn)
	if err != nil {
		serverStatus.Error = fmt.Sprintf("RCON auth response failed: %v", err)
		return serverStatus, err
	}
	if resp.requestID == -1 {
		authErr := fmt.Errorf("RCON authentication failed")
		serverStatus.Error = authErr.Error()
		return serverStatus, authErr
	}

	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	rconReceive(conn)

	serverStatus.Online = true

	if err := rconSend(conn, 2, rconExec, "/players"); err != nil {
		return serverStatus, nil
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	playerResp, err := rconReceive(conn)
	if err != nil {
		return serverStatus, nil
	}

	playerNames := parseFactorioPlayers(playerResp.body)
	serverStatus.Players = len(playerNames)
	serverStatus.PlayerList = make([]Player, 0, len(playerNames))
	for _, name := range playerNames {
		serverStatus.PlayerList = append(serverStatus.PlayerList, Player{Name: name})
	}

	if err := rconSend(conn, 3, rconExec, "/version"); err != nil {
		return serverStatus, nil
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	versionResp, err := rconReceive(conn)
	if err == nil {
		serverStatus.Version = strings.TrimSpace(versionResp.body)
	}

	return serverStatus, nil
}

type rconPacket struct {
	requestID  int32
	packetType int32
	body       string
}

func rconSend(conn net.Conn, requestID, packetType int32, body string) error {
	bodyBytes := []byte(body)
	size := int32(4 + 4 + len(bodyBytes) + 2)

	if err := binary.Write(conn, binary.LittleEndian, size); err != nil {
		return err
	}
	if err := binary.Write(conn, binary.LittleEndian, requestID); err != nil {
		return err
	}
	if err := binary.Write(conn, binary.LittleEndian, packetType); err != nil {
		return err
	}
	if _, err := conn.Write(bodyBytes); err != nil {
		return err
	}
	_, err := conn.Write([]byte{0x00, 0x00})
	return err
}

func rconReceive(conn net.Conn) (*rconPacket, error) {
	reader := bufio.NewReader(conn)

	var size int32
	if err := binary.Read(reader, binary.LittleEndian, &size); err != nil {
		return nil, err
	}

	if size < 10 || size > 4096 {
		return nil, fmt.Errorf("invalid rcon packet size: %d", size)
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, err
	}

	requestID := int32(binary.LittleEndian.Uint32(payload[0:4]))
	packetType := int32(binary.LittleEndian.Uint32(payload[4:8]))
	body := string(payload[8 : size-2])

	return &rconPacket{
		requestID:  requestID,
		packetType: packetType,
		body:       body,
	}, nil
}

func parseFactorioPlayers(output string) []string {
	var players []string
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, "(online)") {
			name := strings.TrimSpace(strings.TrimSuffix(line, "(online)"))
			if name != "" {
				players = append(players, name)
			}
		}
	}
	return players
}
