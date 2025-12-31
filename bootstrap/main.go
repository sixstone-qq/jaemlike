package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
)

const defaultListeningPort = 12347

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	port := defaultListeningPort
	if len(os.Args) > 1 {
		if p, err := strconv.Atoi(os.Args[1]); err == nil {
			port = p
		} else {
			log.Fatalf("invalid port %q: %v", os.Args[1], err)
		}
	}
	if envPort := os.Getenv("BOOTSTRAP_PORT"); envPort != "" {
		if p, err := strconv.Atoi(envPort); err == nil {
			port = p
		} else {
			log.Fatalf("invalid BOOTSTRAP_PORT %q: %v", envPort, err)
		}
	}

	privKey, err := identityFromSeed(seedFromEnvOrPort(port))
	if err != nil {
		log.Fatalf("cannot generate private key: %v", err)
	}

	listenAddr, err := multiaddr.NewMultiaddr(fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", port))
	if err != nil {
		log.Fatalf("cannot build listen addr: %v", err)
	}

	h, err := libp2p.New(
		libp2p.ListenAddrs(listenAddr),
		libp2p.Identity(privKey),
		libp2p.AddrsFactory(filterPrivateAddrs),
	)
	if err != nil {
		log.Fatalf("cannot start libp2p host: %v", err)
	}
	defer h.Close()

	kademliaDHT, err := dht.New(ctx, h, dht.Mode(dht.ModeServer))
	if err != nil {
		log.Fatalf("cannot create DHT: %v", err)
	}
	if err := kademliaDHT.Bootstrap(ctx); err != nil {
		log.Fatalf("cannot bootstrap DHT: %v", err)
	}

	printBootstrapAddrs(h)

	<-ctx.Done()
	fmt.Println("Shutting down...")
}

func seedFromEnvOrPort(port int) string {
	if seed := os.Getenv("BOOTSTRAP_SEED"); seed != "" {
		return seed
	}
	return strconv.Itoa(port)
}

func identityFromSeed(seed string) (crypto.PrivKey, error) {
	sum := sha256.Sum256([]byte(seed))
	priv, _, err := crypto.GenerateEd25519Key(bytes.NewReader(sum[:]))
	return priv, err
}

func filterPrivateAddrs(addrs []multiaddr.Multiaddr) []multiaddr.Multiaddr {
	var filtered []multiaddr.Multiaddr
	for _, addr := range addrs {
		addrStr := addr.String()
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
}

func printBootstrapAddrs(h host.Host) {
	fmt.Println("bootstrap host ID", h.ID(), "addresses", h.Addrs())
	for _, addr := range h.Addrs() {
		fmt.Println(addr.Encapsulate(multiaddr.StringCast("/p2p/" + h.ID().String())).String())
	}
}
