// Command dyno is the dyno speed test server: one static binary serving the
// measurement endpoints, the JSON API and the embedded web UI.
//
// Configuration is by environment so compose.yaml needs no files:
//
//	DYNO_LISTEN  address to listen on (default :3966, "DYNO" on a phone keypad)
//	DYNO_DATA    directory for dyno.db (default ./data; /data in the image)
package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/measure"
	"github.com/redlinelabs-dev/dyno/server/internal/netcap"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
	"github.com/redlinelabs-dev/dyno/server/internal/testrun"
	"github.com/redlinelabs-dev/dyno/server/internal/webui"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Hard caps (ADR-0007).
const (
	defaultListen     = ":3966"
	defaultData       = "data"
	maxConns          = 256
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	stallTimeout      = 10 * time.Second
	shutdownGrace     = 5 * time.Second
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "ping the local server and exit 0 if it answers (for container health checks)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	listen := cmp.Or(os.Getenv("DYNO_LISTEN"), defaultListen)

	switch {
	case *showVersion:
		fmt.Println(version)
		return
	case *healthcheck:
		if err := check(listen); err != nil {
			fmt.Fprintln(os.Stderr, "dyno: healthcheck:", err)
			os.Exit(1)
		}
		return
	}

	ln, err := net.Listen("tcp", listen)
	if err != nil {
		log.Fatal(err)
	}
	st, err := store.Open(cmp.Or(os.Getenv("DYNO_DATA"), defaultData))
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	srv := newServer(version, stallTimeout, st)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := srv.Serve(netcap.Limit(ln, maxConns)); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()
	log.Printf("dyno %s listening on %s", version, ln.Addr())

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		srv.Close() // a test still running past the grace period is cut off
	}
}

func newServer(version string, stall time.Duration, st *store.Store) *http.Server {
	runs := testrun.NewRegistry()
	mux := http.NewServeMux()
	(&measure.Endpoints{Stall: stall, Runs: runs}).Register(mux)
	(&api.API{Version: version, Store: st, Runs: runs}).Register(mux)
	mux.Handle("GET /", webui.Handler())
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    16 << 10,
	}
}

// check GETs /ping from the server listening on listen. The image is FROM
// scratch, so there is no curl for Docker's HEALTHCHECK to use.
func check(listen string) error {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return err
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + "/ping")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("/ping returned %s", resp.Status)
	}
	return nil
}
