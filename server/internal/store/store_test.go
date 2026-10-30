package store

import (
	"errors"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
)

func open(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sample(down float64) result.Result {
	r := result.Result{
		At:     time.Date(2026, 10, 30, 14, 7, 0, 0, time.UTC),
		Source: "browser",
		Submission: result.Submission{
			Client: "Chrome on Linux", TestID: "abc", DownloadMbps: down, UploadMbps: 400,
			IdleMs: 8, LoadedDownMs: 20, LoadedUpMs: 30, JitterMs: 0.5, Streams: 5, ClientLimited: true,
			Detail: []byte(`{"timeline":[1,2,3]}`),
		},
		ServerBytesDown: 1 << 30, ServerBytesUp: 1 << 29,
	}
	r.Derive()
	return r
}

func TestAddGetRoundTrip(t *testing.T) {
	s := open(t, t.TempDir())
	in, err := s.Add(sample(940))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(in.At) || got.DownloadMbps != 940 || got.Grade != "B" || !got.ClientLimited ||
		got.ServerBytesDown != 1<<30 || string(got.Detail) != `{"timeline":[1,2,3]}` {
		t.Fatalf("round trip lost data:\n in %+v\ngot %+v", in, got)
	}
	if _, err := s.Get(in.ID + 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing id: err %v", err)
	}
}

func TestListNewestFirstAndPages(t *testing.T) {
	s := open(t, t.TempDir())
	for i := range 5 {
		if _, err := s.Add(sample(float64(100 + i))); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(2, 0)
	if err != nil || len(page) != 2 || page[0].DownloadMbps != 104 || page[1].DownloadMbps != 103 {
		t.Fatalf("first page %+v, err %v", page, err)
	}
	if page[0].Detail != nil {
		t.Error("List returned detail")
	}
	next, _ := s.List(10, page[1].ID)
	if len(next) != 3 || next[0].DownloadMbps != 102 {
		t.Fatalf("second page %+v", next)
	}
}

func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.Add(sample(1))
	s.Close()
	got, _ := open(t, dir).List(10, 0)
	if len(got) != 1 {
		t.Fatalf("after reopen: %d results", len(got))
	}
}
