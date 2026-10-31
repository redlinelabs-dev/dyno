//go:build !race

package measure

// raceEnabled reports whether the test binary was built with -race.
const raceEnabled = false
