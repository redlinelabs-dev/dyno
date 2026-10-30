// Package webui serves the embedded web UI. dist/ holds the Bun build of web/
// (`make web`); it is not in git.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var dist embed.FS

// Handler serves the files in dist/, with index.html at /.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServerFS(sub)
}
