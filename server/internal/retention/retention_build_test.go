package retention_test

import (
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/redlinelabs-dev/dyno/server/internal/result"
	"github.com/redlinelabs-dev/dyno/server/internal/retention"
	"github.com/redlinelabs-dev/dyno/server/internal/settings"
	"github.com/redlinelabs-dev/dyno/server/internal/store"
)

// hxResult is a client result stored at the given time.
func hxResult(at time.Time) result.Result {
	r := result.Result{At: at, Source: "client"}
	r.Client = "Chrome on Linux"
	r.DownloadMbps = 100
	return r
}

// A keep_days past its limit, left in the table by a newer version or a hand,
// would overflow the duration Prune counts back with and put the cut in the
// future, where it would delete every result. Prune clamps it to the limit.
func TestHx_PruneClampsAKeepDaysPastTheLimit(t *testing.T) {
	const day = 24 * time.Hour
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ages := []time.Duration{0, day, 400 * day, 3650 * day}
	for _, age := range ages {
		if _, err := st.Add(hxResult(now.Add(-age))); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := sql.Open("sqlite", filepath.Join(dir, "dyno.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES ('keep_days', '9999999999')`)
	raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	st, err = store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	n, err := retention.Prune(st, now)
	if err != nil || n != 0 {
		t.Fatalf("Prune with keep_days 9999999999: deleted %d, err %v, want 0 and nil", n, err)
	}
	left, err := st.List(100, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != len(ages) {
		t.Errorf("%d results left, want all %d", len(left), len(ages))
	}
}

// A context that is done before Run starts means no prune at all.
func TestHx_RunWithADoneContextPrunesNothing(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days := 7
	if _, err := st.PatchSettings(settings.Patch{KeepDays: &days}); err != nil {
		t.Fatal(err)
	}
	old, err := st.Add(hxResult(time.Now().UTC().Add(-30 * 24 * time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	retention.Run(ctx, st, slog.New(slog.DiscardHandler))
	if _, err := st.Get(old.ID); err != nil {
		t.Errorf("a result 30 days old was pruned by a Run that had no time to start: %v", err)
	}
}
