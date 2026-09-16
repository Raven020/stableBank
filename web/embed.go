// Package web embeds the D9 deliverable: a single-page vanilla-JS demo UI
// for the stableBank proof-of-concept. It is served by the integrator's
// main.go via web.Handler() mounted at "/".
package web

import (
	"embed"
	"net/http"
)

// FS holds the embedded static assets of the SPA.
//
//go:embed index.html app.js styles.css
var FS embed.FS

// Handler serves the embedded SPA. Every response carries
// Cache-Control: no-store because the whole point of this deliverable is
// that edits to index.html/app.js/styles.css show up on the next reload
// with no build step and no caching surprises.
func Handler() http.Handler {
	fileServer := http.FileServerFS(FS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fileServer.ServeHTTP(w, r)
	})
}
