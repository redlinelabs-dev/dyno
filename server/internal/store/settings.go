package store

import "github.com/redlinelabs-dev/dyno/server/internal/settings"

// Settings returns the server's settings; one never set has its zero value.
func (s *Store) Settings() (settings.Settings, error) { panic("not implemented") }

// PatchSettings validates p, applies it in one transaction, and returns
// every setting. On an error nothing changes.
func (s *Store) PatchSettings(p settings.Patch) (settings.Settings, error) {
	panic("not implemented")
}
