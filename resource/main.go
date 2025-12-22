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
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"
)

const (
	protocolID           protocol.ID = "/hivenet/sample-app/1.0.0"
	defaultListeningPort int         = 12345
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
		//libp2p.DisableRelay(),
		libp2p.Identity(privKeyObj),
	}

	host, err := libp2p.New(p2pOpts...)
	if err != nil {
		slog.Fatal("cannot start libp2p host", err.Error())
	}

	defer host.Close()

	fmt.Println("host ID", host.ID(), "Addresses", host.Addrs())

	host.SetStreamHandler(protocolID, receiveDataStreamHandler)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	<-ctx.Done()
	fmt.Println("Shutting down...")
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
