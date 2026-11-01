// loadgen drives the dyno bench servers over raw HTTP/1.1 keep-alive connections
// and samples the server process from /proc. Std lib only. Prints one JSON object.
//
//	loadgen dl    -addr A -pid P -c 8 -bytes 33554432 -warmup 2s -dur 8s
//	loadgen ul    -addr A -pid P -c 8 -bytes 33554432 -warmup 2s -dur 8s
//	loadgen ping  -addr A -n 5000
//	loadgen churn -addr A -pid P -n 10000 -c 16
//	loadgen soak  -addr A -pid P -dur 10m -sample 5s
//	loadgen hold  -addr A -pid P -n 1000 -dur 70s
//	loadgen coldstart -addr A -n 20 -- cmd args...
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	addr    = flag.String("addr", "127.0.0.1:8080", "server address")
	pid     = flag.Int("pid", 0, "server pid to sample from /proc")
	conc    = flag.Int("c", 8, "concurrent connections")
	nbytes  = flag.Int64("bytes", 32<<20, "bytes per request")
	warmup  = flag.Duration("warmup", 2*time.Second, "warmup before the measured window")
	dur     = flag.Duration("dur", 8*time.Second, "measured window / soak length")
	count   = flag.Int("n", 5000, "requests or connections")
	sample  = flag.Duration("sample", 5*time.Second, "soak RSS sample interval")
	rate    = flag.Float64("rate", 1, "soak traffic multiplier")
	mbps    = flag.Float64("mbps", 0, "dl/ul: pace the total across all connections to this many Mbit/s (0 = unlimited)")
	payload = func() []byte { b := make([]byte, 1<<20); rand.Read(b); return b }()
)

const clkTck = 100.0 // sysconf(_SC_CLK_TCK) on Linux x86_64/arm64

// ---- /proc sampling ----

type procSample struct {
	CPU     float64 `json:"cpu_s"` // utime+stime seconds
	RSSKiB  int64   `json:"rss_kib"`
	AnonKiB int64   `json:"anon_kib"`
	HWMKiB  int64   `json:"hwm_kib"`
	Threads int64   `json:"threads"`
}

func readProc(p int) procSample {
	var s procSample
	if p == 0 {
		return s
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", p)); err == nil {
		// fields after the ")" of comm; utime is field 14, stime 15 (1-based)
		f := strings.Fields(string(b[strings.LastIndexByte(string(b), ')')+2:]))
		u, _ := strconv.ParseFloat(f[11], 64)
		st, _ := strconv.ParseFloat(f[12], 64)
		s.CPU = (u + st) / clkTck
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", p)); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			switch k {
			case "VmRSS":
				s.RSSKiB = n
			case "RssAnon":
				s.AnonKiB = n
			case "VmHWM":
				s.HWMKiB = n
			case "Threads":
				s.Threads = n
			}
		}
	}
	return s
}

// resetPeak resets VmHWM so it reports the peak of the measured window only.
func resetPeak(p int) {
	if p != 0 {
		os.WriteFile(fmt.Sprintf("/proc/%d/clear_refs", p), []byte("5"), 0)
	}
}

// ---- raw HTTP/1.1 client ----

type conn struct {
	c  net.Conn
	br *bufio.Reader
}

func dial() (*conn, error) {
	c, err := net.Dial("tcp", *addr)
	if err != nil {
		return nil, err
	}
	return &conn{c: c, br: bufio.NewReaderSize(c, 64<<10)}, nil
}

// readResponse reads a response, feeding body byte counts to onData as they arrive.
func (k *conn) readResponse(buf []byte, onData func(int)) (int, error) {
	resp, err := http.ReadResponse(k.br, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 && onData != nil {
			onData(n)
		}
		if err == io.EOF {
			return resp.StatusCode, nil
		}
		if err != nil {
			return resp.StatusCode, err
		}
	}
}

func (k *conn) get(path string, buf []byte, onData func(int)) (int, error) {
	if _, err := io.WriteString(k.c, "GET "+path+" HTTP/1.1\r\nHost: bench\r\n\r\n"); err != nil {
		return 0, err
	}
	return k.readResponse(buf, onData)
}

// post sends n bytes of the client payload; onSent sees bytes as they are written.
func (k *conn) post(n int64, buf []byte, onSent func(int)) (int, error) {
	h := fmt.Sprintf("POST /ul HTTP/1.1\r\nHost: bench\r\nContent-Type: application/octet-stream\r\nContent-Length: %d\r\n\r\n", n)
	if _, err := io.WriteString(k.c, h); err != nil {
		return 0, err
	}
	off := 0
	for n > 0 {
		c := min(int64(256<<10), int64(len(payload)-off), n)
		w, err := k.c.Write(payload[off : off+int(c)])
		if onSent != nil {
			onSent(w)
		}
		if err != nil {
			return 0, err
		}
		n -= c
		off = (off + int(c)) % len(payload)
	}
	return k.readResponse(buf, nil)
}

// postJSON sends a small JSON body to path.
func (k *conn) postJSON(path, body string, buf []byte) (int, error) {
	h := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: bench\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", path, len(body), body)
	if _, err := io.WriteString(k.c, h); err != nil {
		return 0, err
	}
	return k.readResponse(buf, nil)
}

// ---- modes ----

func out(v any) {
	b, _ := json.Marshal(v)
	fmt.Println(string(b))
}

// stream runs c workers doing back-to-back dl or ul requests and measures one window.
func stream(upload bool) {
	var moved, reqs, errs atomic.Int64
	var stop atomic.Bool
	var mu sync.Mutex
	var conns []*conn
	var wg sync.WaitGroup
	for range *conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 256<<10)
			add := func(n int) { moved.Add(int64(n)) }
			if *mbps > 0 { // token bucket per worker: sleep whenever ahead of schedule
				perSec := *mbps * 1e6 / 8 / float64(*conc)
				start, mine := time.Now(), 0.0
				add = func(n int) {
					moved.Add(int64(n))
					mine += float64(n)
					if ahead := time.Duration(mine/perSec*float64(time.Second)) - time.Since(start); ahead > 0 {
						time.Sleep(ahead)
					}
				}
			}
			path := "/dl?bytes=" + strconv.FormatInt(*nbytes, 10)
			for !stop.Load() {
				k, err := dial()
				if err != nil {
					errs.Add(1)
					time.Sleep(10 * time.Millisecond)
					continue
				}
				mu.Lock()
				conns = append(conns, k)
				mu.Unlock()
				for !stop.Load() {
					var st int
					if upload {
						st, err = k.post(*nbytes, buf, add)
					} else {
						st, err = k.get(path, buf, add)
					}
					if err != nil || st != 200 {
						if !stop.Load() {
							errs.Add(1)
						}
						break
					}
					reqs.Add(1)
				}
				k.c.Close()
			}
		}()
	}
	time.Sleep(*warmup)
	b0, p0, t0 := moved.Load(), readProc(*pid), time.Now()
	resetPeak(*pid)
	time.Sleep(*dur)
	b1, p1, t1 := moved.Load(), readProc(*pid), time.Now()
	stop.Store(true)
	mu.Lock()
	for _, k := range conns {
		k.c.Close()
	}
	mu.Unlock()
	wg.Wait()
	secs := t1.Sub(t0).Seconds()
	gb := float64(b1-b0) / 1e9
	cpu := p1.CPU - p0.CPU
	out(map[string]any{
		"mode": map[bool]string{false: "dl", true: "ul"}[upload], "c": *conc, "bytes_per_req": *nbytes, "mbps_target": *mbps,
		"secs": secs, "gb": gb, "gbit_s": gb * 8 / secs, "cpu_s": cpu, "cpu_s_per_gb": cpu / gb,
		"cpu_util_pct": 100 * cpu / secs, "peak_rss_kib": p1.HWMKiB, "rss_end_kib": p1.RSSKiB,
		"threads": p1.Threads, "requests": reqs.Load(), "errors": errs.Load(),
	})
}

func pct(xs []float64, p float64) float64 {
	return xs[min(len(xs)-1, int(p*float64(len(xs))))]
}

func ping() {
	k, err := dial()
	if err != nil {
		fail(err)
	}
	buf := make([]byte, 4096)
	lat := make([]float64, 0, *count)
	for i := range *count + 200 {
		t := time.Now()
		st, err := k.get("/ping", buf, nil)
		d := time.Since(t)
		if err != nil || st != 204 {
			fail(fmt.Errorf("ping: status %d err %v", st, err))
		}
		if i >= 200 {
			lat = append(lat, float64(d.Nanoseconds())/1e3)
		}
	}
	slices.Sort(lat)
	out(map[string]any{"mode": "ping", "n": len(lat), "p50_us": pct(lat, .5), "p90_us": pct(lat, .9),
		"p99_us": pct(lat, .99), "p999_us": pct(lat, .999), "max_us": lat[len(lat)-1]})
}

// shortConn opens a connection, does one GET, and closes it.
func shortConn(path string, buf []byte) error {
	k, err := dial()
	if err != nil {
		return err
	}
	defer k.c.Close()
	st, err := k.get(path, buf, nil)
	if err == nil && st/100 != 2 {
		err = fmt.Errorf("status %d", st)
	}
	return err
}

func churn() {
	before := readProc(*pid)
	var next, errs atomic.Int64
	var wg sync.WaitGroup
	t0 := time.Now()
	for range *conc {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 4096)
			for next.Add(1) <= int64(*count) {
				if shortConn("/ping", buf) != nil {
					errs.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	secs := time.Since(t0).Seconds()
	after := readProc(*pid)
	time.Sleep(3 * time.Second)
	settled := readProc(*pid)
	out(map[string]any{"mode": "churn", "n": *count, "c": *conc, "secs": secs, "conn_per_s": float64(*count) / secs,
		"errors": errs.Load(), "rss_before_kib": before.RSSKiB, "rss_after_kib": after.RSSKiB,
		"rss_settled_kib": settled.RSSKiB, "threads_before": before.Threads, "threads_after": settled.Threads,
		"cpu_s": after.CPU - before.CPU})
}

// soak runs mixed, rate-limited traffic including aborted transfers and samples RSS.
func soak() {
	var stop atomic.Bool
	var ops, errs sync.Map
	inc := func(m *sync.Map, k string) {
		v, _ := m.LoadOrStore(k, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
	}
	var wg sync.WaitGroup
	every := func(name string, perSec float64, workers int, fn func(buf []byte) error) {
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				buf := make([]byte, 256<<10)
				tick := time.NewTicker(time.Duration(float64(time.Second) * float64(workers) / (perSec * *rate)))
				defer tick.Stop()
				for !stop.Load() {
					<-tick.C
					if err := fn(buf); err != nil {
						inc(&errs, name)
					} else {
						inc(&ops, name)
					}
				}
			}()
		}
	}
	// keep-alive download and upload loops on persistent connections
	keepAlive := func(fn func(k *conn, buf []byte) error) func([]byte) error {
		var k *conn
		return func(buf []byte) error {
			var err error
			if k == nil {
				if k, err = dial(); err != nil {
					return err
				}
			}
			if err = fn(k, buf); err != nil {
				k.c.Close()
				k = nil
			}
			return err
		}
	}
	ok := func(st int, err error, want int) error {
		if err == nil && st != want {
			err = fmt.Errorf("status %d", st)
		}
		return err
	}
	for range 2 {
		every("dl_4MiB", 10, 1, keepAlive(func(k *conn, b []byte) error { st, err := k.get("/dl?bytes=4194304", b, nil); return ok(st, err, 200) }))
		every("ul_4MiB", 10, 1, keepAlive(func(k *conn, b []byte) error { st, err := k.post(4<<20, b, nil); return ok(st, err, 200) }))
	}
	// A stored result every 2 s and the history listed every second, as browsers
	// finishing tests and opening History do, so the soak covers SQLite too.
	const stored = `{"test_id":"","client":"loadgen","device":"loadgen-soak","download_mbps":938.2,"upload_mbps":412.6,"idle_ms":8.4,"loaded_down_ms":21,"loaded_up_ms":39.5,"jitter_ms":0.8,"streams":5,"client_limited":false}`
	every("result_post", 0.5, 1, keepAlive(func(k *conn, b []byte) error {
		st, err := k.postJSON("/api/v1/results", stored, b)
		return ok(st, err, 201)
	}))
	every("results_list", 1, 1, keepAlive(func(k *conn, b []byte) error {
		st, err := k.get("/api/v1/results?limit=50", b, nil)
		return ok(st, err, 200)
	}))
	every("ping", 100, 1, keepAlive(func(k *conn, b []byte) error { st, err := k.get("/ping", b, nil); return ok(st, err, 204) }))
	every("short_conn_ping", 50, 2, func(b []byte) error { return shortConn("/ping", b) })
	every("short_conn_index", 10, 1, func(b []byte) error { return shortConn("/", b) })
	every("bad_request", 2, 1, func(b []byte) error { // malformed query; expect 4xx, connection stays usable or closes
		k, err := dial()
		if err != nil {
			return err
		}
		defer k.c.Close()
		st, err := k.get("/dl?bytes=nope", b, nil)
		if err == nil && st/100 != 4 {
			return fmt.Errorf("status %d", st)
		}
		return err
	})
	every("abort_dl", 2, 1, func(b []byte) error { // client disconnects mid-download
		k, err := dial()
		if err != nil {
			return err
		}
		io.WriteString(k.c, "GET /dl?bytes=268435456 HTTP/1.1\r\nHost: bench\r\n\r\n")
		_, err = io.ReadFull(k.br, b[:128<<10])
		k.c.Close()
		return err
	})
	every("abort_ul", 2, 1, func(b []byte) error { // client disconnects mid-upload
		k, err := dial()
		if err != nil {
			return err
		}
		io.WriteString(k.c, "POST /ul HTTP/1.1\r\nHost: bench\r\nContent-Length: 268435456\r\n\r\n")
		_, err = k.c.Write(payload)
		k.c.Close()
		return err
	})
	every("idle_open_close", 2, 1, func(b []byte) error { // connect, send nothing, leave
		c, err := net.Dial("tcp", *addr)
		if err == nil {
			time.Sleep(50 * time.Millisecond)
			c.Close()
		}
		return err
	})

	type pt struct {
		T      float64 `json:"t_s"`
		RSS    int64   `json:"rss_kib"`
		Anon   int64   `json:"anon_kib"`
		Thread int64   `json:"threads"`
		CPU    float64 `json:"cpu_s"`
	}
	var pts []pt
	t0 := time.Now()
	for {
		p := readProc(*pid)
		pts = append(pts, pt{time.Since(t0).Seconds(), p.RSSKiB, p.AnonKiB, p.Threads, p.CPU})
		if time.Since(t0) >= *dur {
			break
		}
		time.Sleep(*sample)
	}
	stop.Store(true)
	wg.Wait()
	time.Sleep(5 * time.Second)
	end := readProc(*pid)
	slope := func(from float64) float64 { // least squares KiB per minute for samples with t >= from
		var n, sx, sy, sxx, sxy float64
		for _, q := range pts {
			if q.T < from {
				continue
			}
			x, y := q.T/60, float64(q.RSS)
			n++
			sx += x
			sy += y
			sxx += x * x
			sxy += x * y
		}
		return (n*sxy - sx*sy) / (n*sxx - sx*sx)
	}
	flat := func(m *sync.Map) map[string]int64 {
		r := map[string]int64{}
		m.Range(func(k, v any) bool { r[k.(string)] = v.(*atomic.Int64).Load(); return true })
		return r
	}
	var maxRSS int64
	for _, q := range pts {
		maxRSS = max(maxRSS, q.RSS)
	}
	out(map[string]any{"mode": "soak", "secs": dur.Seconds(), "slope_kib_per_min_all": slope(0),
		"slope_kib_per_min_after_2m": slope(120), "rss_first_kib": pts[0].RSS, "rss_last_kib": pts[len(pts)-1].RSS,
		"rss_max_kib": maxRSS, "rss_after_stop_kib": end.RSSKiB, "threads_after_stop": end.Threads,
		"cpu_s": end.CPU - pts[0].CPU, "ops": flat(&ops), "errors": flat(&errs), "samples": pts})
}

// hold opens n connections that send half a request header and then stall (slowloris-style),
// samples the server while they are held, and counts how many the server has closed by the end.
func hold() {
	before := readProc(*pid)
	conns := make([]net.Conn, 0, *count)
	for range *count {
		c, err := net.Dial("tcp", *addr)
		if err != nil {
			fail(err)
		}
		io.WriteString(c, "GET /ping HTTP/1.1\r\nHost: bench\r\n")
		conns = append(conns, c)
	}
	time.Sleep(2 * time.Second)
	held := readProc(*pid)
	time.Sleep(*dur)
	// One buffer and a 20 ms deadline per read: allocating in this loop set off a
	// GC that outlasted a 1 ms deadline, so a connection the server had closed
	// long before read as open (the 2026-11-01 flake).
	closed := 0
	buf := make([]byte, 4096)
	for _, c := range conns {
		c.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if n, err := c.Read(buf); n > 0 || err == io.EOF || errors.Is(err, syscall.ECONNRESET) {
			closed++
		}
	}
	// a fresh request must still be served while the stalled connections exist
	freshOK := shortConn("/ping", make([]byte, 4096)) == nil
	end := readProc(*pid)
	for _, c := range conns {
		c.Close()
	}
	time.Sleep(3 * time.Second)
	after := readProc(*pid)
	out(map[string]any{"mode": "hold", "n": *count, "wait_s": dur.Seconds(),
		"rss_before_kib": before.RSSKiB, "rss_held_kib": held.RSSKiB, "threads_held": held.Threads,
		"closed_by_server": closed, "fresh_request_ok": freshOK, "rss_end_kib": end.RSSKiB,
		"rss_released_kib": after.RSSKiB, "threads_released": after.Threads})
}

func coldstart(argv []string) {
	if len(argv) == 0 {
		fail(errors.New("coldstart needs -- cmd args"))
	}
	var ms []float64
	buf := make([]byte, 4096)
	for range *count {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		t0 := time.Now()
		if err := cmd.Start(); err != nil {
			fail(err)
		}
		for {
			if k, err := dial(); err == nil {
				st, err := k.get("/ping", buf, nil)
				k.c.Close()
				if err == nil && st == 204 {
					break
				}
			}
			if time.Since(t0) > 10*time.Second {
				fail(errors.New("server did not come up"))
			}
			time.Sleep(200 * time.Microsecond)
		}
		ms = append(ms, float64(time.Since(t0).Microseconds())/1e3)
		cmd.Process.Signal(syscall.SIGKILL)
		cmd.Wait()
		time.Sleep(50 * time.Millisecond)
	}
	slices.Sort(ms)
	out(map[string]any{"mode": "coldstart", "n": len(ms), "median_ms": pct(ms, .5), "min_ms": ms[0], "max_ms": ms[len(ms)-1]})
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loadgen:", err)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 2 {
		fail(errors.New("usage: loadgen dl|ul|ping|churn|soak|coldstart [flags]"))
	}
	mode := os.Args[1]
	flag.CommandLine.Parse(os.Args[2:])
	switch mode {
	case "dl":
		stream(false)
	case "ul":
		stream(true)
	case "ping":
		ping()
	case "churn":
		churn()
	case "soak":
		soak()
	case "hold":
		hold()
	case "coldstart":
		coldstart(flag.Args())
	default:
		fail(fmt.Errorf("unknown mode %q", mode))
	}
}
