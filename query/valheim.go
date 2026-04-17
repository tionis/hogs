package query

import (
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/tionis/hogs/database"
)

type ValheimQuerier struct{}

func (q *ValheimQuerier) Query(server *database.Server) (*ServerStatus, error) {
	serverStatus := &ServerStatus{
		Online:      false,
		LastUpdated: time.Now(),
	}

	host := server.Address
	port := 2457

	if strings.Contains(host, ":") {
		parts := strings.Split(host, ":")
		if len(parts) == 2 {
			host = parts[0]
			p, err := strconv.ParseUint(parts[1], 10, 16)
			if err == nil {
				port = int(p)
			}
		}
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	conn, err := net.DialTimeout("udp", addr, 5*time.Second)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to connect to valheim server: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	challenge, err := q.sendA2SChallenge(conn)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to get A2S challenge: %w", err)
	}

	resp, err := q.sendA2SPlayerQuery(conn, challenge)
	if err != nil {
		serverStatus.Error = err.Error()
		return serverStatus, fmt.Errorf("failed to query valheim server: %w", err)
	}

	serverStatus.Online = true
	serverStatus.Players = resp.NumPlayers
	serverStatus.MaxPlayers = resp.MaxPlayers
	serverStatus.Version = resp.Version
	serverStatus.ServerMessage = resp.Name

	return serverStatus, nil
}

type a2sInfoResponse struct {
	Name       string
	MapName    string
	Folder     string
	Game       string
	Version    string
	NumPlayers int
	MaxPlayers int
}

func (q *ValheimQuerier) sendA2SChallenge(conn net.Conn) (uint32, error) {
	req := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54, 0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20, 0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20, 0x51, 0x75, 0x65, 0x72, 0x79, 0x00}
	if _, err := conn.Write(req); err != nil {
		return 0, err
	}

	buf := make([]byte, 1400)
	n, err := conn.Read(buf)
	if err != nil {
		return 0, err
	}

	if n < 5 {
		return 0, fmt.Errorf("response too short")
	}

	if buf[0] != 0xFF || buf[1] != 0xFF || buf[2] != 0xFF || buf[3] != 0xFF {
		return 0, fmt.Errorf("invalid response header")
	}

	if buf[4] == 0x49 {
		return 0xFFFFFFFF, nil
	}

	if buf[4] == 0x41 {
		if n < 9 {
			return 0, fmt.Errorf("challenge response too short")
		}
		return binary.LittleEndian.Uint32(buf[5:9]), nil
	}

	return 0xFFFFFFFF, nil
}

func (q *ValheimQuerier) sendA2SPlayerQuery(conn net.Conn, challenge uint32) (*a2sInfoResponse, error) {
	req := make([]byte, 25)
	copy(req, []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54, 0x53, 0x6F, 0x75, 0x72, 0x63, 0x65, 0x20, 0x45, 0x6E, 0x67, 0x69, 0x6E, 0x65, 0x20, 0x51, 0x75, 0x65, 0x72, 0x79, 0x00})
	binary.LittleEndian.PutUint32(req[21:], challenge)

	offset := 21
	req = append(req[:offset], byte(0x00))
	binary.LittleEndian.PutUint32(req[offset+1:], challenge)

	req2 := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x54}
	req2 = append(req2, []byte("Source Engine Query\x00")...)
	req2 = binary.LittleEndian.AppendUint32(req2, challenge)

	if _, err := conn.Write(req2); err != nil {
		return nil, err
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, err
	}

	if n < 6 {
		return nil, fmt.Errorf("response too short")
	}

	if buf[0] != 0xFF || buf[1] != 0xFF || buf[2] != 0xFF || buf[3] != 0xFF {
		return nil, fmt.Errorf("invalid response header")
	}

	if buf[4] != 0x49 {
		return nil, fmt.Errorf("unexpected response type: 0x%02x", buf[4])
	}

	pos := 5

	name, _, err := readCString(buf, pos)
	if err != nil {
		return nil, err
	}
	pos += len(name) + 1

	mapName, _, err := readCString(buf, pos)
	if err != nil {
		return nil, err
	}
	pos += len(mapName) + 1

	folder, _, err := readCString(buf, pos)
	if err != nil {
		return nil, err
	}
	pos += len(folder) + 1

	game, _, err := readCString(buf, pos)
	if err != nil {
		return nil, err
	}
	pos += len(game) + 1

	if pos+2 > n {
		return nil, fmt.Errorf("response too short for steam id")
	}
	pos += 2

	if pos+8 > n {
		return nil, fmt.Errorf("response too short for player counts")
	}

	numPlayers := int(buf[pos])
	pos++
	maxPlayers := int(buf[pos])
	pos++

	_ = pos

	var version string
	if pos < n && buf[pos] != 0 {
		v, _, _ := readCString(buf, pos)
		version = v
	}

	return &a2sInfoResponse{
		Name:       name,
		MapName:    mapName,
		Folder:     folder,
		Game:       game,
		Version:    version,
		NumPlayers: numPlayers,
		MaxPlayers: maxPlayers,
	}, nil
}

func readCString(buf []byte, offset int) (string, int, error) {
	end := offset
	for end < len(buf) && buf[end] != 0 {
		end++
	}
	if end >= len(buf) {
		return "", 0, fmt.Errorf("unterminated string")
	}
	return string(buf[offset:end]), end - offset + 1, nil
}
