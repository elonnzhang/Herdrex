// Command herdrex exposes a Herdr server as a Rex host.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/elonnzhang/herdrex/internal/bridge"
)

func main() {
	sock := flag.String("herdr-socket", os.Getenv("HERDR_SOCKET_PATH"), "Herdr API socket path")
	listen := flag.String("listen", "127.0.0.1:7392", "loopback address to serve Rex on")
	flag.Parse()
	if *sock == "" {
		log.Fatal("set --herdr-socket or HERDR_SOCKET_PATH")
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		log.Fatal("--listen must be a loopback ip:port; the bridge has no authentication")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	b := bridge.New(*sock)
	if err := b.Refresh(ctx); err != nil {
		log.Fatalf("cannot reach Herdr at %s: %v", *sock, err)
	}
	go b.Watch(ctx)
	go b.Poll(ctx, 5*time.Second) // safety net if an event is missed
	srv := &http.Server{Addr: *listen, Handler: b.Handler()}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("herdrex listening on http://%s (herdr: %s)", *listen, *sock)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
