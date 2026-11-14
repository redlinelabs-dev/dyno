// Package webui serves the embedded web UI. dist/ holds the Bun build of web/
// (`make web`); it is not in git.
package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
)

//go:embed dist
var dist embed.FS

// Go's MIME table has no entry for the web app manifest.
func init() { mime.AddExtensionType(".webmanifest", "application/manifest+json") }

// Handler serves the files in dist/, with index.html at /.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return http.FileServerFS(sub)
}
