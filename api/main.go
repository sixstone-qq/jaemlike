package main

import (
	"fmt"
	"io"
	"log"
	"net/http"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const (
	port                   = 12345
	protocolID protocol.ID = "/hivenet/sample-app/1.0.0"
)

func main() {
	s, err := newServer()
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()

	http.HandleFunc("/data", s.handlePostData)

	log.Println("Server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

type Server struct {
	host     host.Host
	targetID peer.ID
}

func newServer() (*Server, error) {
	host, err := libp2p.New()
	if err != nil {
		return nil, fmt.Errorf("can't create server: %w", err)
	}

	targetPeer, err := peer.AddrInfoFromString(fmt.Sprintf("/dns4/resource/tcp/%d/p2p/%s", port, "12D3KooWGA94TgYHH338vdMzos8R8oLP8rk84DGzYfCR4KN717VL"))
	if err != nil {
		return nil, fmt.Errorf("cannot get target peer: %w", err)
	}
	host.Peerstore().AddAddr(targetPeer.ID, targetPeer.Addrs[0], peerstore.PermanentAddrTTL)

	return &Server{
		host:     host,
		targetID: targetPeer.ID,
	}, nil
}

func (s *Server) Close() error {
	return s.host.Close()
}

func (s *Server) handlePostData(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	stream, err := s.host.NewStream(r.Context(), s.targetID, protocolID)
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
