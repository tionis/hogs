package wgnet

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

const defaultMTU = 1420

type Config struct {
	Address    string `json:"address" yaml:"address"`
	PrivateKey string `json:"privateKey" yaml:"private_key"`
	ListenPort int    `json:"listenPort" yaml:"listen_port"`
	MTU        int    `json:"mtu" yaml:"mtu"`
	Peers      []Peer `json:"peers" yaml:"peers"`
}

type Peer struct {
	PublicKey           string `json:"publicKey" yaml:"public_key"`
	AllowedIP           string `json:"allowedIP" yaml:"allowed_ip"`
	Endpoint            string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	PersistentKeepalive int    `json:"persistentKeepalive,omitempty" yaml:"persistent_keepalive,omitempty"`
}

type Network struct {
	dev *device.Device
	net *netstack.Net
}

func New(cfg Config, logPrefix string) (*Network, error) {
	address, err := netip.ParseAddr(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("parse WireGuard address: %w", err)
	}
	if len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("at least one WireGuard peer is required")
	}
	privateKey, err := keyHex(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse WireGuard private key: %w", err)
	}
	mtu := cfg.MTU
	if mtu == 0 {
		mtu = defaultMTU
	}
	tun, tnet, err := netstack.CreateNetTUN([]netip.Addr{address}, nil, mtu)
	if err != nil {
		return nil, fmt.Errorf("create userspace network stack: %w", err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelError, logPrefix))

	var ipc strings.Builder
	fmt.Fprintf(&ipc, "private_key=%s\nlisten_port=%d\n", privateKey, cfg.ListenPort)
	for _, peer := range cfg.Peers {
		publicKey, err := keyHex(peer.PublicKey)
		if err != nil {
			dev.Close()
			return nil, fmt.Errorf("parse peer public key: %w", err)
		}
		allowed, err := netip.ParsePrefix(peer.AllowedIP)
		if err != nil {
			dev.Close()
			return nil, fmt.Errorf("parse peer allowed_ip: %w", err)
		}
		fmt.Fprintf(&ipc, "public_key=%s\nallowed_ip=%s\n", publicKey, allowed)
		if peer.Endpoint != "" {
			fmt.Fprintf(&ipc, "endpoint=%s\n", peer.Endpoint)
		}
		if peer.PersistentKeepalive > 0 {
			fmt.Fprintf(&ipc, "persistent_keepalive_interval=%d\n", peer.PersistentKeepalive)
		}
	}
	if err := dev.IpcSet(ipc.String()); err != nil {
		dev.Close()
		return nil, fmt.Errorf("configure WireGuard device: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("start WireGuard device: %w", err)
	}
	return &Network{dev: dev, net: tnet}, nil
}

func (n *Network) Close() {
	if n != nil && n.dev != nil {
		n.dev.Close()
	}
}

func (n *Network) ListenTCP(port uint16) (net.Listener, error) {
	return n.net.ListenTCPAddrPort(netip.AddrPortFrom(netip.Addr{}, port))
}

func (n *Network) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("unsupported network %q", network)
	}
	addr, err := netip.ParseAddrPort(address)
	if err != nil {
		return nil, err
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := n.net.DialTCPAddrPort(addr)
		ch <- result{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-ch:
		return result.conn, result.err
	}
}

func keyHex(value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("expected 32 bytes, got %d", len(raw))
	}
	return hex.EncodeToString(raw), nil
}
