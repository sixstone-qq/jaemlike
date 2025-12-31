package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	slog "log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/libp2p/go-libp2p/p2p/discovery/util"
	"github.com/multiformats/go-multiaddr"
)

const (
	protocolID           protocol.ID = "/hivenet/sample-app/1.0.0"
	defaultListeningPort int         = 12345
	rendezVous           string      = "meet-at-the-harbour"
	defaultBootstrapHost             = "bootstrap"
	defaultBootstrapPort             = 12347
)

func main() {
	var err error
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	port := defaultListeningPort
	if len(os.Args) > 1 {
		port, err = strconv.Atoi(os.Args[1])
		if err != nil {
			slog.Fatal("failed to parse ", os.Args[1])
		}
	}

	// Use port as randomness to generate same hostID all the time
	privKeyObj, _, err := identityFromPort(port)
	if err != nil {
		slog.Fatal("cannot generate private key", err.Error())
	}

	// 0.0.0.0 will listen on any interface device.
	sourceMultiAddr, _ := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))

	p2pOpts := []libp2p.Option{
		libp2p.ListenAddrs(sourceMultiAddr),
		libp2p.Identity(privKeyObj),
		// Only advertise private network addresses (Docker network)
		libp2p.AddrsFactory(func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
			var filtered []multiaddr.Multiaddr
			for _, addr := range addrs {
				// Keep only private network ranges: 10.x, 172.16-31.x, 192.168.x
				if isPrivateNetwork(addr) {
					filtered = append(filtered, addr)
				}
			}
			return filtered
		}),
	}

	host, err := libp2p.New(p2pOpts...)
	if err != nil {
		slog.Fatal("cannot start libp2p host", err.Error())
	}

	defer host.Close()

	fmt.Println("host ID", host.ID(), "Addresses", host.Addrs())

	host.SetStreamHandler(protocolID, receiveDataStreamHandler)

	err = initDHT(ctx, host, rendezVous)
	if err != nil {
		slog.Fatal("cannot init DHT:", err.Error())
	}
	<-ctx.Done()
	fmt.Println("Shutting down...")

}

func identityFromPort(port int) (crypto.PrivKey, crypto.PubKey, error) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(port))

	seed := sha256.Sum256(b[:]) // 32 bytes
	return crypto.GenerateEd25519Key(bytes.NewReader(seed[:]))
}

func isPrivateNetwork(addr multiaddr.Multiaddr) bool {
	s := addr.String()
	// Allow private IPv4 ranges:
	// 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16
	return bytes.Contains([]byte(s), []byte("/ip4/10.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.16.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.17.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.18.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.19.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.20.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.21.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.22.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.23.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.24.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.25.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.26.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.27.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.28.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.29.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.30.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/172.31.")) ||
		bytes.Contains([]byte(s), []byte("/ip4/192.168."))
}

func receiveDataStreamHandler(stream network.Stream) {
	fmt.Println("stream ID", stream.ID())
	defer stream.Close()

	buf := bufio.NewReader(stream)
	l, err := buf.ReadByte()
	if err != nil {
		fmt.Println("reading byte", err.Error())
		return
	}
	lr := io.LimitReader(buf, int64(l))
	data, err := io.ReadAll(lr)
	if err != nil {
		fmt.Println("reading data", err.Error())
		return
	}
	fmt.Println("data", string(data), "length", l)

	_, err = stream.Write([]byte("nice!"))
	if err != nil {
		fmt.Println("writing data", err.Error())
		return
	}
}

func initDHT(ctx context.Context, peerHost host.Host, rendezvous string) error {
	// Create DHT in server mode so peers can find each other
	kademliaDHT, err := dht.New(ctx, peerHost, dht.Mode(dht.ModeServer))
	if err != nil {
		return fmt.Errorf("cannot create DHT: %w", err)
	}

	// Connect to bootstrap nodes (prefer private bootstrap if configured)
	bootstrapPeers, source, err := bootstrapPeersFromEnv()
	if err != nil {
		return fmt.Errorf("cannot load bootstrap peers: %w", err)
	}
	if len(bootstrapPeers) == 0 {
		bootstrapPeers = dht.GetDefaultBootstrapPeerAddrInfos()
		source = "default public"
	}

	var connectedCount int
	for _, peerInfo := range bootstrapPeers {
		if err := peerHost.Connect(ctx, peerInfo); err == nil {
			connectedCount++
		}
	}
	fmt.Printf("Connected to %d bootstrap peers (%s)\n", connectedCount, source)

	// Bootstrap the DHT
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return fmt.Errorf("cannot bootstrap DHT: %w", err)
	}

	// Give bootstrap some time to complete
	time.Sleep(2 * time.Second)

	// Announce our presence
	fmt.Println("Announcing ourselves on the DHT")
	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)
	util.Advertise(ctx, routingDiscovery, rendezvous)

	// Give advertisement time to propagate
	time.Sleep(5 * time.Second)

	go findPeers(ctx, peerHost, kademliaDHT, routingDiscovery, rendezvous)

	return nil
}

func findPeers(ctx context.Context, host host.Host, kademliaDHT *dht.IpfsDHT, routingDiscovery *routing.RoutingDiscovery, rendezvous string) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if i == 0 {
				ticker.Reset(30 * time.Second)
			}
			i++
			peerChan, err := routingDiscovery.FindPeers(ctx, rendezvous)
			if err != nil {
				fmt.Println("ERR: cannot find peers", err)
				continue
			}

			select {
			case <-ctx.Done():
				return
			case peer, ok := <-peerChan: // will block until we discover a peer
				if !ok {
					fmt.Println("discovery channel is closed")
					return
				}
				// Validate peer information before attempting connection
				if peer.ID == "" || peer.ID == host.ID() {
					continue
				}

				// If no addresses, try to find them in the DHT
				if len(peer.Addrs) == 0 {
					fmt.Printf("Peer %s has no addresses, looking up in DHT...\n", peer.ID)
					ctx2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
					peerInfo, err := kademliaDHT.FindPeer(ctx2, peer.ID)
					cancel2()
					if err != nil {
						fmt.Printf("Failed to find peer %s in DHT: %v\n", peer.ID, err)
						continue
					}
					peer = peerInfo
					fmt.Printf("Found addresses for peer %s: %v\n", peer.ID, peer.Addrs)
				}

				if len(peer.Addrs) == 0 {
					fmt.Println("Peer still has no addresses after DHT lookup:", peer.ID)
					continue
				}

				fmt.Println("Found peer:", peer, ", connecting")

				if err := host.Connect(ctx, peer); err != nil {
					fmt.Println("Connection failed:", err)
					continue
				}
				fmt.Println("Connected")
			}
		}
	}
}

func bootstrapPeersFromEnv() ([]peer.AddrInfo, string, error) {
	if addrs := strings.TrimSpace(os.Getenv("BOOTSTRAP_ADDRS")); addrs != "" {
		infos := make([]peer.AddrInfo, 0, 4)
		for _, addr := range strings.Split(addrs, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			maddr, err := multiaddr.NewMultiaddr(addr)
			if err != nil {
				return nil, "", fmt.Errorf("invalid BOOTSTRAP_ADDRS entry %q: %w", addr, err)
			}
			info, err := peer.AddrInfoFromP2pAddr(maddr)
			if err != nil {
				return nil, "", fmt.Errorf("invalid BOOTSTRAP_ADDRS entry %q: %w", addr, err)
			}
			infos = append(infos, *info)
		}
		return infos, "env BOOTSTRAP_ADDRS", nil
	}

	seed := strings.TrimSpace(os.Getenv("BOOTSTRAP_SEED"))
	if seed == "" {
		return nil, "", nil
	}
	host := strings.TrimSpace(os.Getenv("BOOTSTRAP_HOST"))
	if host == "" {
		host = defaultBootstrapHost
	}
	portStr := strings.TrimSpace(os.Getenv("BOOTSTRAP_PORT"))
	port := defaultBootstrapPort
	if portStr != "" {
		parsedPort, err := strconv.Atoi(portStr)
		if err != nil {
			return nil, "", fmt.Errorf("invalid BOOTSTRAP_PORT %q: %w", portStr, err)
		}
		port = parsedPort
	}
	peerID, err := peerIDFromSeed(seed)
	if err != nil {
		return nil, "", fmt.Errorf("cannot derive bootstrap peer ID: %w", err)
	}
	maddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/dns4/%s/tcp/%d/p2p/%s", host, port, peerID))
	if err != nil {
		return nil, "", fmt.Errorf("cannot build bootstrap addr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, "", fmt.Errorf("cannot parse bootstrap addr: %w", err)
	}
	return []peer.AddrInfo{*info}, "env BOOTSTRAP_SEED", nil
}

func peerIDFromSeed(seed string) (peer.ID, error) {
	sum := sha256.Sum256([]byte(seed))
	priv, _, err := crypto.GenerateEd25519Key(bytes.NewReader(sum[:]))
	if err != nil {
		return "", err
	}
	return peer.IDFromPrivateKey(priv)
}
