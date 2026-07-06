// Package webui serves the browser client: an xterm.js terminal speaking
// the same websocket protocol as `cotty join`, with end-to-end encryption
// handled in the page via WebCrypto. The assets are embedded, so every
// relay (and locally hosted session) serves its own copy — no CDN, no
// external requests.
package webui

import (
	"embed"
	"net/http"
)

//go:embed index.html static
var files embed.FS

// Handler serves the join page at / and /join, and the vendored assets
// under /static/.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.FileServer(http.FS(files)))
	index := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" && r.URL.Path != "/join" {
			http.NotFound(w, r)
			return
		}
		http.ServeFileFS(w, r, files, "index.html")
	}
	mux.HandleFunc("/", index)
	return mux
}
