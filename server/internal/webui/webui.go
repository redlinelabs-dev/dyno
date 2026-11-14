// Package webui serves the embedded web UI. In M1 dist/ holds one hand-written
// page; from M2 it holds the Bun build of web/.
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
