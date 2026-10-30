// dyno bench server: Go, net/http, standard library only.
package main

import (
	"crypto/rand"
	_ "embed"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

//go:embed index.html
var indexHTML []byte

const (
	payloadSize = 1 << 20   // 1 MiB of random bytes, generated once
	writeChunk  = 256 << 10 // bytes handed to the socket per write
	maxDownload = 1 << 34   // 16 GiB sanity cap
)

var payload = func() []byte {
	b := make([]byte, payloadSize)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}()

var readBufs = sync.Pool{New: func() any { b := make([]byte, 64<<10); return &b }}

func download(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil || n < 0 || n > maxDownload {
		http.Error(w, "bad bytes", http.StatusBadRequest)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	w.WriteHeader(http.StatusOK)
	off := 0
	for n > 0 {
		c := min(int64(writeChunk), int64(payloadSize-off), n)
		if _, err := w.Write(payload[off : off+int(c)]); err != nil {
			return // client went away
		}
		n -= c
		off = (off + int(c)) % payloadSize
	}
}

func upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	bp := readBufs.Get().(*[]byte)
	defer readBufs.Put(bp)
	var total int64
	for {
		k, err := r.Body.Read(*bp)
		total += int64(k)
		if err == io.EOF {
			break
		}
		if err != nil {
			return
		}
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, strconv.FormatInt(total, 10))
}

func ping(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(indexHTML)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dl", download)
	mux.HandleFunc("POST /ul", upload)
	mux.HandleFunc("GET /ping", ping)
	mux.HandleFunc("GET /", index)
	srv := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Fatal(srv.ListenAndServe())
}
