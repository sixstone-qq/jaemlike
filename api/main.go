package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/signal"
	"syscall"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const (
	port                   = 12345
	protocolID protocol.ID = "/hivenet/sample-app/1.0.0"
	rendezVous string      = "meet-at-the-harbour"
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
	host host.Host
}

func newServer(ctx context.Context) (*Server, error) {
	host, err := libp2p.New()
	if err != nil {
		return nil, fmt.Errorf("can't create server: %w", err)
	}

	peerCh, mdnsSvc, err := initMDNS(host)
	if err != nil {
		return nil, fmt.Errorf("can't start mDNS server: %w", err)
	}

	go func() {
		for {
			select {
			case <-ctx.Done():
				mdnsSvc.Close()
				return
			case p := <-peerCh:
				if p.ID == host.ID() {
					// Discarding our own peer :)
					continue
				}
				addrs := filterOutSelfAddrs(host, p.Addrs)

				fmt.Println("Adding peer", p.ID, addrs)
				host.Peerstore().AddAddrs(p.ID, addrs, peerstore.PermanentAddrTTL)
			}
		}
	}()

	return &Server{
		host: host,
	}, nil
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

func initMDNS(peerhost host.Host) (chan peer.AddrInfo, mdns.Service, error) {
	// register with service so that we get notified about peer discovery
	n := &discoveryNotifee{}
	n.PeerChan = make(chan peer.AddrInfo)

	// An hour might be a long long period in practical applications. But this is fine for us
	svc := mdns.NewMdnsService(peerhost, rendezVous, n)
	if err := svc.Start(); err != nil {
		return nil, nil, err
	}

	fmt.Printf("mDNS service started for rendezvous: %s\n", rendezVous)

	return n.PeerChan, svc, nil
}

type discoveryNotifee struct {
	PeerChan chan peer.AddrInfo
}

// interface to be called when new  peer is found
func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.PeerChan <- pi
}

func (s *Server) Close() error {
	return s.host.Close()
}

func (s *Server) selectPeer() peer.ID {
	peers := s.host.Peerstore().Peers()
	for _, p := range peers {
		if p != s.host.ID() {
			return p
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
