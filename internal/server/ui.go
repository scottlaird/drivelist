package server

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed ui
var uiFiles embed.FS

// mermaidSource is where the page may load Mermaid from, for the SAS
// topology diagram; ui/app.js pins the exact file under it.
const mermaidSource = "https://cdn.jsdelivr.net/npm/mermaid@11.17.2/"

// UIPolicy is the Content-Security-Policy the web interface runs under,
// less frame-ancestors, which only a header can carry; a static copy of
// the page (see internal/demo) puts it in a meta tag.
const UIPolicy = "default-src 'none'; script-src 'self' " + mermaidSource + "; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'none'"

// UIFile returns one of the web interface's files, for writing a static
// copy of it.
func UIFile(name string) ([]byte, error) {
	return uiFiles.ReadFile("ui/" + name)
}

// uiHandler serves the web interface: static files, embedded, under a
// Content-Security-Policy that admits only same-origin scripts (plus
// Mermaid from its pinned CDN path) and no inline script, so nothing
// stored in the database (a drive whose model is a script tag) can
// become code in the page. The page itself never puts data into HTML;
// see ui/app.js. Inline styles are allowed because Mermaid styles the
// SVG it draws that way; they cannot run code.
func uiHandler() http.Handler {
	sub, err := fs.Sub(uiFiles, "ui")
	if err != nil {
		panic(err)
	}
	files := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", UIPolicy+"; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
