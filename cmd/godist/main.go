package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"godist/internal/api"
	"godist/internal/cluster"
	"godist/internal/store"
	"godist/internal/transport"
)

func main() {
	var (
		id      = flag.String("id", "", "node ID (string; higher wins elections)")
		addr    = flag.String("addr", "127.0.0.1:8081", "listen address host:port")
		seed    = flag.String("seed", "", "optional seed node host:port to join")
		n       = flag.Int("N", 3, "replication factor")
		r       = flag.Int("R", 2, "read quorum")
		w       = flag.Int("W", 2, "write quorum")
		vnodes  = flag.Int("vnodes", 16, "virtual nodes per physical node")
		epoch   = flag.Uint64("epoch", 1, "node epoch (bump on restart to override stale address)")
		logJSON = flag.Bool("logjson", false, "log in JSON (minimal)")
		chaos   = flag.Bool("chaos", false, "enable chaos mode: randomly delay/drop outbound requests (incl. heartbeats)")
	)
	flag.Parse()

	if *id == "" {
		// Keep it deterministic and readable for local demos.
		rand.Seed(time.Now().UnixNano())
		*id = fmt.Sprintf("node-%d", 1000+rand.Intn(9000))
	}

	if *logJSON {
		log.SetFlags(0)
		log.SetPrefix("")
	}

	cli := transport.NewClient(800 * time.Millisecond)
	if *chaos {
		cli.EnableChaos(transport.DefaultChaosConfig())
		log.Printf("CHAOS enabled: outbound requests may be delayed/dropped")
	}
	cl := cluster.New(*id, *addr, *epoch, *vnodes, cli)
	st := store.New()
	srv := api.NewServer(cl, st, *n, *r, *w)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cl.Start(ctx)

	if *seed != "" {
		ctx2, cancel2 := context.WithTimeout(ctx, 3*time.Second)
		if err := cl.Join(ctx2, *seed); err != nil {
			log.Printf("join failed seed=%s err=%v", *seed, err)
		} else {
			log.Printf("joined via seed=%s", *seed)
		}
		cancel2()
	}

	h := srv.Handler()
	httpServer := &http.Server{Addr: *addr, Handler: h}

	go func() {
		log.Printf("starting %s on %s (N=%d R=%d W=%d)", *id, *addr, *n, *r, *w)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 2)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Printf("shutting down")
	ctxShutdown, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	_ = httpServer.Shutdown(ctxShutdown)
	cancelShutdown()
}
