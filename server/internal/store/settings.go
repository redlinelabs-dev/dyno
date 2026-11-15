package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/redlinelabs-dev/dyno/server/internal/settings"
)

// The settings table's keys are the settings' JSON names, and each value is
// that setting as JSON (migration 4).
const (
	keyPlanDown = "plan_down_mbps"
	keyPlanUp   = "plan_up_mbps"
	keyKeepDays = "keep_days"
)

// Settings returns the server's settings; one never set has its zero value.
func (s *Store) Settings() (settings.Settings, error) { return readSettings(s.db) }

// PatchSettings validates p, applies it in one transaction, and returns
// every setting. On an error nothing changes.
func (s *Store) PatchSettings(p settings.Patch) (settings.Settings, error) {
	if err := p.Validate(); err != nil {
		return settings.Settings{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return settings.Settings{}, err
	}
	defer tx.Rollback() // a no-op once the transaction has committed
	cur, err := readSettings(tx)
	if err != nil {
		return settings.Settings{}, err
	}
	next := cur.Apply(p) // what is written is what Apply makes of the patch, so a -0 is stored as 0
	// Only the fields sent are written; every other stored value stays as it is.
	put := func(key string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT OR REPLACE INTO settings (key, value) VALUES (?, ?)`, key, string(b))
		return err
	}
	if p.PlanDownMbps != nil {
		if err := put(keyPlanDown, next.PlanDownMbps); err != nil {
			return settings.Settings{}, err
		}
	}
	if p.PlanUpMbps != nil {
		if err := put(keyPlanUp, next.PlanUpMbps); err != nil {
			return settings.Settings{}, err
		}
	}
	if p.KeepDays != nil {
		if err := put(keyKeepDays, next.KeepDays); err != nil {
			return settings.Settings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return settings.Settings{}, err
	}
	return next, nil
}

// settingsQuerier is what *sql.DB and *sql.Tx share, so one reader serves both.
type settingsQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// readSettings reads every setting. A key this version doesn't know, such as
// one a newer version wrote, is skipped. A value that isn't the JSON it
// should be is an error, so nothing acts on it. A value out of range is not:
// it comes back as stored, and retention clamps it where it matters.
func readSettings(q settingsQuerier) (settings.Settings, error) {
	rows, err := q.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return settings.Settings{}, err
	}
	defer rows.Close()
	var out settings.Settings
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return settings.Settings{}, err
		}
		switch key {
		case keyPlanDown:
			err = json.Unmarshal([]byte(value), &out.PlanDownMbps)
		case keyPlanUp:
			err = json.Unmarshal([]byte(value), &out.PlanUpMbps)
		case keyKeepDays:
			err = json.Unmarshal([]byte(value), &out.KeepDays)
		}
		if err != nil {
			return settings.Settings{}, fmt.Errorf("reading setting %s: %w", key, err)
		}
	}
	return out, rows.Err()
}
