// Command dyno is the dyno speed test server: one static binary serving the
// measurement endpoints, the JSON API and the embedded web UI.
//
// Configuration is by environment so compose.yaml needs no files:
//
//	DYNO_LISTEN          address to listen on (default :3966, "DYNO" on a phone keypad)
//	DYNO_DATA            directory for dyno.db (default ./data; /data in the image)
//	DYNO_METRICS_LISTEN  address for Prometheus GET /metrics (default off; never the
//	                     main port, ADR-0017), for example 127.0.0.1:3967
//
// Logs are JSON lines on stdout (ADR-0020).
package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/api"
	"github.com/redlinelabs-dev/dyno/server/internal/measure"
	"github.com/redlinelabs-dev/dyno/server/internal/metrics"
	"github.com/redlinelabs-dev/dyno/server/internal/netcap"
	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/scheduler"
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
	maxMetricsConns   = 8 // a scraper or two, never a crowd
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 60 * time.Second
	stallTimeout      = 10 * time.Second
	metricsTimeout    = 10 * time.Second // whole request; a scrape is one small reply
	shutdownGrace     = 5 * time.Second
)

// config is the server's environment.
type config struct {
	listen        string
	metricsListen string // empty: no metrics listener
	data          string
}

func configFromEnv(getenv func(string) string) config {
	return config{
		listen:        cmp.Or(getenv("DYNO_LISTEN"), defaultListen),
		metricsListen: getenv("DYNO_METRICS_LISTEN"),
		data:          cmp.Or(getenv("DYNO_DATA"), defaultData),
	}
}

func main() {
	healthcheck := flag.Bool("healthcheck", false, "ping the local server and exit 0 if it answers (for container health checks)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	cfg := configFromEnv(os.Getenv)

	switch {
	case *showVersion:
		fmt.Println(version)
		return
	case *healthcheck:
		if err := check(cfg.listen); err != nil {
			fmt.Fprintln(os.Stderr, "dyno: healthcheck:", err)
			os.Exit(1)
		}
		return
	}

	// The default logger too, so anything still using the log package
	// (net/http's own messages, a dependency) comes out as JSON.
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, cfg, logger, nil)
	stop()
	if err != nil {
		os.Exit(1) // run has logged why
	}
}

// run serves until ctx is done or a listener fails. ready, if set, is called
// once both listeners are up; metricsAddr is nil when metrics are off. Every
// failure is logged here before it is returned.
func run(ctx context.Context, cfg config, log *slog.Logger, ready func(addr, metricsAddr net.Addr)) error {
	started := time.Now()
	startFailed := func(code string, err error) error {
		log.LogAttrs(ctx, slog.LevelError, "server.start",
			slog.String("result", "failed"),
			slog.String("version", version),
			slog.String("listen_address", cfg.listen),
			slog.String("metrics_listen_address", cfg.metricsListen),
			slog.String("data_dir", cfg.data),
			slog.String("error.code", code),
			slog.String("error.message", err.Error()))
		return err
	}

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		return startFailed("listen_failed", err)
	}
	// The operator asked for metrics, so a metrics address that can't be
	// bound stops the server rather than leaving the scrape silently empty.
	var mln net.Listener
	if cfg.metricsListen != "" {
		if mln, err = net.Listen("tcp", cfg.metricsListen); err != nil {
			ln.Close()
			return startFailed("metrics_listen_failed", err)
		}
	}
	st, err := store.Open(cfg.data)
	if err != nil {
		ln.Close()
		if mln != nil {
			mln.Close()
		}
		return startFailed("store_open_failed", err)
	}
	defer st.Close()

	// The scheduler and the retention job run under bg, which run cancels
	// on every path. Both have returned before server.stop is logged, so no
	// line follows it; this defer, registered after st.Close's and so run
	// first, is the same guarantee for a path that returns before that.
	bg, stopBg := context.WithCancel(context.Background())
	var bgDone sync.WaitGroup
	defer func() {
		stopBg()
		bgDone.Wait()
	}()

	m := metrics.New(version)
	sched := &scheduler.Scheduler{
		Store:   st,
		Test:    nodetest.Run,
		Version: version,
		Busy:    m.Serving,
		Metrics: m,
		Log:     log,
	}
	srv := newServerWith(version, stallTimeout, st, m, log, scheduling{sched: sched, check: endsWith(bg, checkFarEnd), stop: bg})
	bgDone.Add(2)
	go func() {
		defer bgDone.Done()
		sched.Run(bg)
	}()
	go func() {
		defer bgDone.Done()
		retention.Run(bg, st, log)
	}()
	servers := []*http.Server{srv}
	errc := make(chan error, 2)
	go func() { errc <- srv.Serve(netcap.Limit(ln, maxConns)) }()
	var metricsAddr net.Addr
	if mln != nil {
		msrv := newMetricsServer(m, log)
		servers = append(servers, msrv)
		metricsAddr = mln.Addr()
		go func() { errc <- msrv.Serve(netcap.Limit(mln, maxMetricsConns)) }()
	}
	metricsListen := ""
	if metricsAddr != nil {
		metricsListen = metricsAddr.String()
	}
	log.LogAttrs(ctx, slog.LevelInfo, "server.start",
		slog.String("result", "succeeded"),
		slog.String("version", version),
		slog.String("listen_address", ln.Addr().String()),
		slog.String("metrics_listen_address", metricsListen),
		slog.String("data_dir", cfg.data),
		slog.Float64("duration_ms", float64(time.Since(started).Microseconds())/1e3))
	if ready != nil {
		ready(ln.Addr(), metricsAddr)
	}

	select {
	case <-ctx.Done():
	case err := <-errc: // Serve returns before Shutdown only when it fails
		stopBg()
		bgDone.Wait()
		for _, s := range servers {
			s.Close()
		}
		log.LogAttrs(context.Background(), slog.LevelError, "server.stop",
			slog.String("result", "failed"),
			slog.Float64("uptime_seconds", time.Since(started).Seconds()),
			slog.String("error.code", "serve_failed"),
			slog.String("error.message", err.Error()))
		return err
	}

	// A scheduled run is stopped first so it can't hold the grace period, and
	// the background goroutines are waited for before server.stop is logged, so
	// no line of theirs comes after it.
	stopBg()
	bgDone.Wait()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	var shutdownErr error
	for _, s := range servers {
		if err := s.Shutdown(sctx); err != nil {
			s.Close() // a test still running past the grace period is cut off
			shutdownErr = err
		}
	}
	attrs := []slog.Attr{slog.Float64("uptime_seconds", time.Since(started).Seconds())}
	if shutdownErr != nil {
		// Expected when a test outlives the grace period; nothing to act on.
		log.LogAttrs(context.Background(), slog.LevelWarn, "server.stop", append(attrs,
			slog.String("result", "failed"),
			slog.String("error.code", "shutdown_timeout"),
			slog.String("error.message", shutdownErr.Error()))...)
		return nil
	}
	log.LogAttrs(context.Background(), slog.LevelInfo, "server.stop", append(attrs, slog.String("result", "succeeded"))...)
	return nil
}

// scheduling is what the API needs to run schedules. The zero value leaves
// the schedule endpoints answering 503, as a server with no scheduler does.
type scheduling struct {
	sched api.Scheduler
	check func(ctx context.Context, farEnd string) (nodetest.FarEndInfo, error)
	stop  context.Context // done once the server begins stopping (nil: never)
}

// checkFarEnd reads farEnd's plan without running a test (ADR-0028).
func checkFarEnd(ctx context.Context, farEnd string) (nodetest.FarEndInfo, error) {
	return nodetest.Check(ctx, nodetest.Options{FarEnd: farEnd})
}

// endsWith is check with its context also cancelled once stop is done, so a
// check waiting on a far end that never answers ends when shutdown starts, and
// the handler can answer within the grace period.
func endsWith(stop context.Context, check func(context.Context, string) (nodetest.FarEndInfo, error)) func(context.Context, string) (nodetest.FarEndInfo, error) {
	return func(ctx context.Context, farEnd string) (nodetest.FarEndInfo, error) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		defer context.AfterFunc(stop, cancel)()
		return check(ctx, farEnd)
	}
}

// newServer builds the main listener's server with no scheduler and no
// far-end check. m and log may be nil.
func newServer(version string, stall time.Duration, st *store.Store, m *metrics.Metrics, log *slog.Logger) *http.Server {
	return newServerWith(version, stall, st, m, log, scheduling{})
}

// newServerWith is newServer plus the scheduler and the far-end check.
func newServerWith(version string, stall time.Duration, st *store.Store, m *metrics.Metrics, log *slog.Logger, sc scheduling) *http.Server {
	if log == nil {
		log = slog.Default()
	}
	runs := testrun.NewRegistry()
	mux := http.NewServeMux()
	(&measure.Endpoints{Stall: stall, Runs: runs, Metrics: m}).Register(mux)
	(&api.API{Version: version, Store: st, Runs: runs, Metrics: m, Log: log, Scheduler: sc.sched, Check: sc.check, Stop: sc.stop}).Register(mux)
	mux.Handle("GET /", webui.Handler())
	return &http.Server{
		Handler:           m.Instrument(mux),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// newMetricsServer serves GET /metrics on its own listener (ADR-0017).
func newMetricsServer(m *metrics.Metrics, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           m.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       metricsTimeout,
		WriteTimeout:      metricsTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
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
