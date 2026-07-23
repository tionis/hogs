package wgnet

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/curve25519"
)

func TestUserspacePeersExchangeTCP(t *testing.T) {
	serverPrivate := testKey(1)
	clientPrivate := testKey(2)
	serverPort := freeUDPPort(t)
	server, err := New(Config{
		Address: "fd42:686f:6773::1", PrivateKey: serverPrivate,
		ListenPort: serverPort,
		Peers:      []Peer{{PublicKey: publicKey(t, clientPrivate), AllowedIP: "fd42:686f:6773::2/128"}},
	}, "test-server: ")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := New(Config{
		Address: "fd42:686f:6773::2", PrivateKey: clientPrivate,
		Peers: []Peer{{
			PublicKey: publicKey(t, serverPrivate), AllowedIP: "fd42:686f:6773::1/128",
			Endpoint: net.JoinHostPort("127.0.0.1", itoa(serverPort)), PersistentKeepalive: 1,
		}},
	}, "test-client: ")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listener, err := server.ListenTCP(9081)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		payload := make([]byte, 4)
		if _, err := io.ReadFull(conn, payload); err != nil {
			done <- err
			return
		}
		_, err = conn.Write(payload)
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, "tcp", "[fd42:686f:6773::1]:9081")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte("hogs")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "hogs" {
		t.Fatalf("response = %q", response)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func testKey(seed byte) string {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func publicKey(t *testing.T, private string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(private)
	if err != nil {
		t.Fatal(err)
	}
	public, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(public)
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var buffer [20]byte
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[index:])
}
