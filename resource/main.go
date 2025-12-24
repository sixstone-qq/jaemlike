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
	"syscall"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/discovery/mdns"
	"github.com/multiformats/go-multiaddr"
)

const (
	protocolID           protocol.ID = "/hivenet/sample-app/1.0.0"
	defaultListeningPort int         = 12345
	rendezVous           string      = "meet-at-the-harbour"
)

func main() {
	var err error
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
	}

	host, err := libp2p.New(p2pOpts...)
	if err != nil {
		slog.Fatal("cannot start libp2p host", err.Error())
	}

	defer host.Close()

	fmt.Println("host ID", host.ID(), "Addresses", host.Addrs())

	host.SetStreamHandler(protocolID, receiveDataStreamHandler)

	peerChan, err := initMDNS(host, rendezVous)
	if err != nil {
		slog.Fatal("cannot init mDNS")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	for { // allows multiple peers to join
		select {
		case <-ctx.Done():
			fmt.Println("Shutting down...")
			return
		case peer := <-peerChan: // will block until we discover a peer
			if peer.ID >= host.ID() {
				// if other end peer id greater than us, don't connect to it, just wait for it to connect us
				fmt.Println("Found peer:", peer, " id is greater than us, wait for it to connect to us")
				continue
			}
			fmt.Println("Found peer:", peer, ", connecting")

			if err := host.Connect(ctx, peer); err != nil {
				fmt.Println("Connection failed:", err)
				continue
			}
		}
	}
}

func identityFromPort(port int) (crypto.PrivKey, crypto.PubKey, error) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(port))

	seed := sha256.Sum256(b[:]) // 32 bytes
	return crypto.GenerateEd25519Key(bytes.NewReader(seed[:]))
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

func initMDNS(peerhost host.Host, rendezvous string) (chan peer.AddrInfo, error) {
	// register with service so that we get notified about peer discovery
	n := &discoveryNotifee{}
	n.PeerChan = make(chan peer.AddrInfo)

	// An hour might be a long long period in practical applications. But this is fine for us
	ser := mdns.NewMdnsService(peerhost, rendezvous, n)
	if err := ser.Start(); err != nil {
		return nil, err
	}

	fmt.Printf("mDNS service started for rendezvous: %s\n", rendezvous)

	return n.PeerChan, nil
}

type discoveryNotifee struct {
	PeerChan chan peer.AddrInfo
}

// interface to be called when new  peer is found
func (n *discoveryNotifee) HandlePeerFound(pi peer.AddrInfo) {
	n.PeerChan <- pi
}
