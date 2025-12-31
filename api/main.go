package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
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
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/routing"
	"github.com/multiformats/go-multiaddr"
)

const (
	port                             = 12345
	protocolID           protocol.ID = "/hivenet/sample-app/1.0.0"
	rendezVous           string      = "meet-at-the-harbour"
	defaultBootstrapHost             = "bootstrap"
	defaultBootstrapPort             = 12347
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	s, err := newServer(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	mux := http.NewServeMux()

	mux.HandleFunc("POST /data", s.handlePostData)

	log.Println("Server starting on :8080")
	httpSrv := &http.Server{
		Addr:    ":8080",
		Handler: mux,
	}
	defer httpSrv.Close()

	go func() {
		if err := httpSrv.ListenAndServe(); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				log.Fatal(err)
			}
		}
	}()

	<-ctx.Done()
	fmt.Println("Shutting down...")
}

type Server struct {
	host          host.Host
	kademliaDHT   *dht.IpfsDHT
	resourcePeers map[peer.ID]struct{} // Track discovered resource peers
}

func newServer(ctx context.Context) (*Server, error) {
	// Only advertise private network addresses
	host, err := libp2p.New(
		libp2p.AddrsFactory(func(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
			var filtered []multiaddr.Multiaddr
			for _, addr := range addrs {
				addrStr := addr.String()
				// Keep only private network ranges
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
	)
	if err != nil {
		return nil, fmt.Errorf("can't create server: %w", err)
	}

	fmt.Println("API host ID", host.ID(), "Addresses", host.Addrs())

	srv := &Server{
		host:          host,
		resourcePeers: make(map[peer.ID]struct{}),
	}

	kademliaDHT, peerCh, err := initDHT(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("can't start DHT client: %w", err)
	}
	srv.kademliaDHT = kademliaDHT

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case p, ok := <-peerCh:
				if !ok {
					// Channel closed, no more peers
					fmt.Println("Peer discovery channel closed")
					return
				}
				if p.ID == "" || p.ID == host.ID() {
					continue
				}

				// If no addresses, try to find them in the DHT
				if len(p.Addrs) == 0 {
					fmt.Printf("Peer %s has no addresses, looking up in DHT...\n", p.ID)
					ctx2, cancel2 := context.WithTimeout(ctx, 10*time.Second)
					peerInfo, err := kademliaDHT.FindPeer(ctx2, p.ID)
					cancel2()
					if err != nil {
						fmt.Printf("Failed to find peer %s in DHT: %v\n", p.ID, err)
						continue
					}
					p = peerInfo
				}

				if len(p.Addrs) == 0 {
					continue
				}

				addrs := filterOutSelfAddrs(host, p.Addrs)
				fmt.Println("Discovered peer", p.ID, "with addresses:", addrs)
				host.Peerstore().AddAddrs(p.ID, addrs, peerstore.PermanentAddrTTL)

				// Actually connect to the peer
				fmt.Printf("Connecting to peer %s...\n", p.ID)
				if err := host.Connect(ctx, p); err != nil {
					fmt.Printf("Failed to connect to peer %s: %v\n", p.ID, err)
					continue
				}
				fmt.Printf("Successfully connected to peer %s\n", p.ID)

				// Verify the peer supports our protocol
				protocols, err := host.Peerstore().SupportsProtocols(p.ID, protocolID)
				if err != nil || len(protocols) == 0 {
					fmt.Printf("Peer %s does not support protocol %s\n", p.ID, protocolID)
					continue
				}
				fmt.Printf("Peer %s supports protocol %s\n", p.ID, protocolID)

				// Mark this as a resource peer
				srv.resourcePeers[p.ID] = struct{}{}
			}
		}
	}()

	return srv, nil
}

func filterOutSelfAddrs(h host.Host, addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	self := map[string]struct{}{}
	for _, a := range h.Addrs() {
		self[a.String()] = struct{}{}
	}
	out := addrs[:0]
	for _, a := range addrs {
		if _, ok := self[a.String()]; ok {
			continue
		}
		out = append(out, a)
	}
	return out
}

func initDHT(ctx context.Context, peerHost host.Host) (*dht.IpfsDHT, <-chan peer.AddrInfo, error) {
	// Create DHT in client mode (doesn't store DHT data, only queries)
	kademliaDHT, err := dht.New(ctx, peerHost, dht.Mode(dht.ModeClient))
	if err != nil {
		return nil, nil, fmt.Errorf("cannot create DHT: %w", err)
	}

	// Connect to bootstrap nodes (prefer private bootstrap if configured)
	bootstrapPeers, source, err := bootstrapPeersFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("cannot load bootstrap peers: %w", err)
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
		return nil, nil, fmt.Errorf("cannot bootstrap DHT: %w", err)
	}

	// Give bootstrap time to complete
	time.Sleep(2 * time.Second)

	// Look for peers advertising at the rendezvous point
	fmt.Println("Searching for peers on DHT...")
	routingDiscovery := routing.NewRoutingDiscovery(kademliaDHT)

	// Create a buffered channel to prevent blocking
	bufferedPeerChan := make(chan peer.AddrInfo, 10)

	go func() {
		// Continuously search for peers with rate limiting
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer close(bufferedPeerChan)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				fmt.Println("Searching for new peers...")
				searchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				peerChan, err := routingDiscovery.FindPeers(searchCtx, rendezVous)
				if err != nil {
					cancel()
					fmt.Printf("Error finding peers: %v\n", err)
					continue
				}

				// Read from the search results
				for p := range peerChan {
					select {
					case bufferedPeerChan <- p:
					case <-ctx.Done():
						cancel()
						return
					}
				}
				cancel()
			}
		}
	}()

	fmt.Printf("DHT client started for rendezvous: %s\n", rendezVous)

	return kademliaDHT, bufferedPeerChan, nil
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

func (s *Server) Close() error {
	return s.host.Close()
}

func (s *Server) selectPeer() peer.ID {
	// Only select from discovered resource peers (stored in PeerStore), not bootstrap nodes
	peers := s.host.Peerstore().Peers()
	for _, p := range peers {
		// Skip self and non-resource peers
		if p == s.host.ID() {
			continue
		}
		// Only select if it's a resource peer
		if _, isResource := s.resourcePeers[p]; isResource {
			// Check if we're still connected
			if s.host.Network().Connectedness(p) == 1 { // Connected
				// Verify peer supports our protocol
				protocols, err := s.host.Peerstore().SupportsProtocols(p, protocolID)
				if err == nil && len(protocols) > 0 {
					return p
				}
			}
		}
	}
	return ""
}

func (s *Server) handlePostData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	peerID := s.selectPeer()
	if peerID == "" {
		http.Error(w, "Starting up", http.StatusServiceUnavailable)
		return
	}

	stream, err := s.host.NewStream(r.Context(), peerID, protocolID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to open stream %v", err), http.StatusInternalServerError)
		return
	}
	defer stream.Close()

	fmt.Println("Sending data to stream", stream.ID())

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	fmt.Printf("Received %d bytes\n", len(body))

	_, err = stream.Write([]byte{byte(len(body))})
	if err != nil {
		http.Error(w, "Failed to write to peer", http.StatusInternalServerError)
		return
	}

	_, err = stream.Write(body)
	if err != nil {
		http.Error(w, "Failed to write to peer", http.StatusInternalServerError)
		return
	}

	peerRes, err := io.ReadAll(stream)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read the peer response %v", err), http.StatusInternalServerError)
		return
	}

	fmt.Println("Received", string(peerRes), "from peer")

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Successfully received %d bytes\n", len(body))
}
