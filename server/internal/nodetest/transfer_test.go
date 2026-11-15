package nodetest_test

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/nodetest"
)

func TestN16_EachDirectionOpensExactlyItsStreamsAndDownloadComesFirst(t *testing.T) {
	b := theBaseline(t) // 3 download streams, 2 upload streams
	if got := b.peaks["/x/dl"]; got != 3 {
		t.Errorf("most /dl in flight at once = %d, want 3", got)
	}
	if got := b.peaks["/x/ul"]; got != 2 {
		t.Errorf("most /ul in flight at once = %d, want 2", got)
	}
	dl, ul := on(b.recs, "/x/dl"), on(b.recs, "/x/ul")
	if len(dl) == 0 || len(ul) == 0 {
		t.Fatalf("%d /dl and %d /ul requests", len(dl), len(ul))
	}
	var lastEnd time.Time
	for _, r := range dl {
		if r.End.After(lastEnd) {
			lastEnd = r.End
		}
	}
	// The client's own view: it reports the upload phase when the download is
	// over, so no /ul can arrive before that.
	first := ul[0].Start
	if up := b.res.firstAt(nodetest.PhaseUpload); first.Before(up) {
		t.Errorf("the first /ul arrived %v before Progress reported the upload phase", up.Sub(first))
	}
	// The far end's: it notices an abandoned download a moment after the
	// client drops it, so allow 100 ms.
	if first.Add(100 * time.Millisecond).Before(lastEnd) {
		t.Errorf("the first /ul came %v before the last /dl ended", lastEnd.Sub(first))
	}
}

func TestN17_EveryTransferCarriesTheTestIDAndAFreshCacheBuster(t *testing.T) {
	b := theBaseline(t)
	seen := map[string]bool{}
	n := 0
	for _, r := range b.recs {
		if r.Path != "/x/dl" && r.Path != "/x/ul" {
			continue
		}
		n++
		if got := r.Query.Get("t"); got != testID {
			t.Errorf("%s ?t=%q, want the plan's test_id %q", r.Path, got, testID)
		}
		switch buster := r.Query.Get("r"); {
		case buster == "":
			t.Errorf("%s has no r= cache buster", r.Path)
		case seen[buster]:
			t.Errorf("r=%s was sent twice", buster)
		default:
			seen[buster] = true
		}
	}
	if n == 0 {
		t.Fatal("no /dl or /ul requests")
	}
}

// dlSizes is what each /dl asked for, in arrival order: with one stream, the
// order it asked in.
func dlSizes(rs []rec) []int64 {
	var out []int64
	for _, r := range on(rs, "/dl") {
		out = append(out, r.size())
	}
	return out
}

func TestN18_DownloadRequestsDoubleWhileUnderTheTarget(t *testing.T) {
	plan := func(start, limit int64, target int) planDoc {
		p := shortPlan()
		p.Download = transferDoc{Streams: 1, DurationMs: 800, WarmupMs: 100, StartBytes: start, MaxBytes: limit, TargetRequestMs: target}
		return p
	}

	// Every request takes far less than the target, so the size doubles to
	// max_bytes and stays there.
	t.Run("fast", func(t *testing.T) {
		fe := newFarEnd(t, plan(64<<10, 1<<20, 1000))
		fe.DLMbps = 200
		fe.start()
		fe.runUntil(t, fe.options(), nodetest.PhaseUpload, 0)
		sizes := dlSizes(fe.recs())
		if len(sizes) < 6 {
			t.Fatalf("%d /dl requests %v, want at least 6", len(sizes), sizes)
		}
		if sizes[0] != 64<<10 {
			t.Errorf("first /dl asked for %d bytes, want start_bytes %d", sizes[0], 64<<10)
		}
		for i := 1; i < len(sizes); i++ {
			if want := min(2*sizes[i-1], int64(1<<20)); sizes[i] != want {
				t.Errorf("/dl %d asked for %d bytes after %d, want %d (double, at most max_bytes)", i, sizes[i], sizes[i-1], want)
			}
		}
	})

	// 0.25 MB/s: 64 KiB takes 262 ms, under the 390 ms target, so the next
	// request doubles; 128 KiB takes 524 ms, over it, so the size holds. Each
	// side of the target is over 125 ms away, room for dialling and scheduling
	// under -race. Four requests take about 1.4 s to start, so the direction
	// runs 2 s.
	t.Run("slow", func(t *testing.T) {
		p := plan(64<<10, 4<<20, 390)
		p.Download.DurationMs = 2000
		fe := newFarEnd(t, p)
		fe.DLMbps = 2
		fe.start()
		fe.runUntil(t, fe.options(), nodetest.PhaseUpload, 0)
		sizes := dlSizes(fe.recs())
		if len(sizes) < 4 {
			t.Fatalf("%d /dl requests %v, want at least 4", len(sizes), sizes)
		}
		if sizes[0] != 64<<10 || sizes[1] != 128<<10 {
			t.Errorf("first two /dl asked for %d and %d bytes, want %d and %d", sizes[0], sizes[1], 64<<10, 128<<10)
		}
		for i := 2; i < len(sizes); i++ {
			if sizes[i] != 128<<10 {
				t.Errorf("/dl %d asked for %d bytes, want 131072: the last took over the target, so the size holds", i, sizes[i])
			}
		}
	})
}

// uploadsOf runs a test with one upload stream against a far end that holds
// each reply until hold after the request opened, and returns the uploads it saw.
func uploadsOf(t *testing.T, up transferDoc, hold time.Duration, count func(int64) int64) []rec {
	t.Helper()
	p := shortPlan()
	p.Download = transferDoc{Streams: 1, DurationMs: 500, WarmupMs: 100, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
	p.Upload = up
	fe := newFarEnd(t, p)
	fe.ULMbps = 0
	fe.ULHold, fe.ULCount = hold, count
	fe.start()
	mustPass(t, fe.runFull(t))
	fe.waitIdle(t)
	ul := on(fe.recs(), "/ul")
	for i, r := range ul {
		if r.Length < 0 || r.Chunked {
			t.Errorf("/ul %d was sent without a Content-Length (chunked: %v)", i, r.Chunked)
		}
		if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
			t.Errorf("/ul %d Content-Type = %q, want application/octet-stream", i, got)
		}
		if r.Length != r.Bytes && !r.Aborted {
			t.Errorf("/ul %d: Content-Length %d but the far end read %d bytes", i, r.Length, r.Bytes)
		}
	}
	return ul
}

func lengths(rs []rec) []int64 {
	var out []int64
	for _, r := range rs {
		out = append(out, r.Length)
	}
	return out
}

func TestN19_UploadRequestsAreSizedFromTheLastReply(t *testing.T) {
	// Replies come 150 ms after each request opens and the target is 750 ms,
	// so each body should be about 5 times the last one, up to max_bytes.
	grow := transferDoc{Streams: 1, DurationMs: 1000, WarmupMs: 200, StartBytes: 50_000, MaxBytes: 400_000, TargetRequestMs: 750}

	t.Run("grows to max_bytes", func(t *testing.T) {
		got := lengths(uploadsOf(t, grow, 150*time.Millisecond, nil))
		if len(got) < 4 {
			t.Fatalf("%d uploads %v, want at least 4", len(got), got)
		}
		if got[0] != 50_000 {
			t.Errorf("first body is %d bytes, want start_bytes 50000", got[0])
		}
		// 50000 over 153 ms times 750 ms is about 245000, and 250000 at most
		// (a reply can't come before 150 ms). The floor, past the 100000 that
		// doubling would give, allows a reply up to 340 ms after the request
		// opened: 190 ms of slack.
		if got[1] < 110_000 || got[1] > 252_000 {
			t.Errorf("second body is %d bytes, want about 245000 (50000 over 153 ms, times 750 ms)", got[1])
		}
		for i := 2; i < len(got); i++ {
			if got[i] != 400_000 {
				t.Errorf("body %d is %d bytes, want max_bytes 400000", i, got[i])
			}
		}
	})

	// The size comes from the bytes the reply says, not the bytes sent.
	t.Run("follows the reply's count", func(t *testing.T) {
		got := lengths(uploadsOf(t, grow, 150*time.Millisecond, func(read int64) int64 { return read / 2 }))
		if len(got) < 2 {
			t.Fatalf("%d uploads %v, want at least 2", len(got), got)
		}
		if got[0] != 50_000 {
			t.Errorf("first body is %d bytes, want start_bytes 50000", got[0])
		}
		// 25000 over 153 ms times 750 ms is about 122500, and 125000 at most.
		// The floor allows a reply up to 310 ms after the request opened:
		// 160 ms of slack. Sizing from the bytes sent would give 245000, and
		// ignoring the reply 50000, both outside.
		if got[1] < 60_000 || got[1] > 126_000 {
			t.Errorf("second body is %d bytes, want about 122500 (25000 over 153 ms, times 750 ms)", got[1])
		}
	})

	// A request that took long against a short target would ask for less than
	// 64 KiB; it asks for 64 KiB.
	t.Run("never under 64 KiB", func(t *testing.T) {
		slow := transferDoc{Streams: 1, DurationMs: 800, WarmupMs: 100, StartBytes: 100_000, MaxBytes: 1 << 20, TargetRequestMs: 100}
		got := lengths(uploadsOf(t, slow, 300*time.Millisecond, nil))
		if len(got) < 3 {
			t.Fatalf("%d uploads %v, want at least 3", len(got), got)
		}
		if got[0] != 100_000 {
			t.Errorf("first body is %d bytes, want start_bytes 100000", got[0])
		}
		for i := 1; i < len(got); i++ {
			if got[i] != 64<<10 {
				t.Errorf("body %d is %d bytes, want 65536 (33000 raised to 64 KiB)", i, got[i])
			}
		}
	})
}

func TestN20_UploadBodiesAreIncompressible(t *testing.T) {
	b := theBaseline(t)
	var stretch []byte
	for _, r := range on(b.recs, "/x/ul") {
		if len(r.Head) >= 1<<20 {
			stretch = r.Head[:1<<20]
			break
		}
	}
	if stretch == nil {
		t.Fatal("the far end kept no 1 MiB of an upload body")
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	zw.Write(stretch)
	zw.Close()
	if ratio := float64(buf.Len()) / float64(len(stretch)); ratio < 0.99 {
		t.Errorf("1 MiB of an upload body gzips to %.3f of its size, want at least 0.99", ratio)
	}
	if bytes.Count(stretch, stretch[:1]) == len(stretch) {
		t.Errorf("1 MiB of an upload body is all byte %#x", stretch[0])
	}
}

func TestN21_DownloadSpeedComesFromBytesReceivedAfterWarmUp(t *testing.T) {
	p := shortPlan()
	p.Download = transferDoc{Streams: 3, DurationMs: 1500, WarmupMs: 300, StartBytes: 128 << 10, MaxBytes: 8 << 20, TargetRequestMs: 200}
	p.Upload.DurationMs = 500
	fe := newFarEnd(t, p)
	fe.DLMbps = 80 // all three streams together
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	near(t, "DownloadMbps", res.Out.Submission.DownloadMbps, 80, 16)
}

// uploadSpeed runs a test against a far end that reads uploads at 40 Mbit/s
// and replies with count's figure for the bytes it read.
func uploadSpeed(t *testing.T, count func(int64) int64) float64 {
	t.Helper()
	p := shortPlan()
	p.Download = transferDoc{Streams: 1, DurationMs: 500, WarmupMs: 100, StartBytes: 64 << 10, MaxBytes: 1 << 20, TargetRequestMs: 100}
	p.Upload = transferDoc{Streams: 2, DurationMs: 1800, WarmupMs: 300, StartBytes: 128 << 10, MaxBytes: 4 << 20, TargetRequestMs: 150}
	fe := newFarEnd(t, p)
	fe.ULMbps, fe.ULCount = 40, count
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	return res.Out.Submission.UploadMbps
}

func TestN22_UploadSpeedComesFromTheFarEndsReplies(t *testing.T) {
	t.Run("honest", func(t *testing.T) {
		near(t, "UploadMbps", uploadSpeed(t, nil), 40, 8)
	})
	t.Run("a reply of half the bytes", func(t *testing.T) {
		near(t, "UploadMbps", uploadSpeed(t, func(read int64) int64 { return read / 2 }), 20, 4)
	})
}

func TestN23_ADirectionEndsWhenItsDurationHasPassed(t *testing.T) {
	p := shortPlan()
	// One 16 MiB request at 1 MB/s: it can't finish, so it's still coming when time is up.
	p.Download = transferDoc{Streams: 1, DurationMs: 1000, WarmupMs: 200, StartBytes: 16 << 20, MaxBytes: 32 << 20, TargetRequestMs: 1000}
	p.Upload.DurationMs = 500
	fe := newFarEnd(t, p)
	fe.DLMbps = 8
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	fe.waitIdle(t)

	got := res.firstAt(nodetest.PhaseUpload).Sub(res.firstAt(nodetest.PhaseDownload))
	if got < 900*time.Millisecond || got > 1400*time.Millisecond {
		t.Errorf("the download phase lasted %v, want 1 s (+0.4 s, -0.1 s)", got)
	}
	dl := on(fe.recs(), "/dl")
	if len(dl) != 1 || !dl[0].Aborted || dl[0].Bytes >= 16<<20 {
		t.Errorf("/dl requests %+v, want the one request abandoned part way", dl)
	}
	if res.Out.Submission.DownloadMbps <= 0 {
		t.Errorf("DownloadMbps = %v, want above 0: the bytes that arrived count", res.Out.Submission.DownloadMbps)
	}
}

func TestN24_WithNoUploadReplyTheClientsCountStands(t *testing.T) {
	p := shortPlan()
	p.Download.DurationMs = 500
	// 64 MiB at 5 MB/s won't be read, let alone answered, in 1.5 s, and it's
	// more than the kernel's socket buffers hold.
	p.Upload = transferDoc{Streams: 1, DurationMs: 1500, WarmupMs: 300, StartBytes: 64 << 20, MaxBytes: 128 << 20, TargetRequestMs: 1000}
	fe := newFarEnd(t, p)
	fe.ULMbps = 40
	fe.start()
	res := fe.runFull(t)
	mustPass(t, res)
	fe.waitIdle(t)

	if !parseDetail(t, res.Out).UploadFromClient {
		t.Errorf(`detail has no "upload_from_client": true; detail: %s`, res.Out.Submission.Detail)
	}
	if got := res.Out.Submission.UploadMbps; got <= 1 {
		t.Errorf("UploadMbps = %v, want the speed of the bytes the client wrote (well above 1)", got)
	}
	for i, r := range on(fe.recs(), "/ul") {
		if !r.Aborted {
			t.Errorf("/ul %d finished and was answered, so the test doesn't show what it should", i)
		}
	}
}
