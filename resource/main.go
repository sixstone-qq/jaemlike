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
)

func main() {
	var err error
	ctx := context.Background()
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
				addrStr := addr.String()
				// Keep only private network ranges: 10.x, 172.16-31.x, 192.168.x
				if strings.HasPrefix(addrStr, "/ip4/10.") ||
					strings.HasPrefix(addrStr, "/ip4/172.16.") ||
					strings.HasPrefix(addrStr, "/ip4/172.17.") ||
					strings.HasPrefix(addrStr, "/ip4/172.18.") ||
					strings.HasPrefix(addrStr, "/ip4/172.19.") ||
					strings.HasPrefix(addrStr, "/ip4/172.20.") ||
					strings.HasPrefix(addrStr, "/ip4/172.21.") ||
					strings.HasPrefix(addrStr, "/ip4/172.22.") ||
					strings.HasPrefix(addrStr, "/ip4/172.23.") ||
					strings.HasPrefix(addrStr, "/ip4/172.24.") ||
					strings.HasPrefix(addrStr, "/ip4/172.25.") ||
					strings.HasPrefix(addrStr, "/ip4/172.26.") ||
					strings.HasPrefix(addrStr, "/ip4/172.27.") ||
					strings.HasPrefix(addrStr, "/ip4/172.28.") ||
					strings.HasPrefix(addrStr, "/ip4/172.29.") ||
					strings.HasPrefix(addrStr, "/ip4/172.30.") ||
					strings.HasPrefix(addrStr, "/ip4/172.31.") ||
					strings.HasPrefix(addrStr, "/ip4/192.168.") {
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

	kademliaDHT, peerChan, err := initDHT(ctx, host, rendezVous)
	if err != nil {
		slog.Fatal("cannot init DHT:", err.Error())
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	for { // allows multiple peers to join
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down...")
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

func initDHT(ctx context.Context, peerHost host.Host, rendezvous string) (*dht.IpfsDHT, <-chan peer.AddrInfo, error) {
	// Create DHT in server mode so peers can find each other
	kademliaDHT, err := dht.New(ctx, peerHost, dht.Mode(dht.ModeServer))
	if err != nil {
		return nil, nil, fmt.Errorf("cannot create DHT: %w", err)
	}

	// Connect to IPFS public bootstrap nodes to join the DHT network
	bootstrapPeers := dht.GetDefaultBootstrapPeerAddrInfos()

	var connectedCount int
	for _, peerInfo := range bootstrapPeers {
		if err := peerHost.Connect(ctx, peerInfo); err == nil {
			connectedCount++
		}
	}
	fmt.Printf("Connected to %d bootstrap peers\n", connectedCount)

	// Bootstrap the DHT
	if err = kademliaDHT.Bootstrap(ctx); err != nil {
		return nil, nil, fmt.Errorf("cannot bootstrap DHT: %w", err)
	}

	// Give bootstrap some time to complete
	time.Sleep(2 * time.Second)

	// Announce our presence
	fmt.Println("Announcing ourselves on the DHT")
	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)
	util.Advertise(ctx, routingDiscovery, rendezvous)

	// Give advertisement time to propagate
	time.Sleep(5 * time.Second)

	// Look for peers advertising the same rendezvous
	fmt.Println("Searching for peers...")
	peerChan, err := routingDiscovery.FindPeers(ctx, rendezvous)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot find peers: %w", err)
	}

	fmt.Printf("DHT service started for rendezvous: %s\n", rendezvous)

	return kademliaDHT, peerChan, nil
}
