package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// webFS is the dashboard, compiled into the binary.
//
// Embedded rather than served from disk so `orrery-server` stays one file to
// copy: an operator who has the binary has the UI, with no asset directory to
// forget and no CDN to reach.
//
//go:embed web
var webFS embed.FS

func (s *Server) uiHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// The embed is compiled in; a failure here means the build is broken,
		// not the deployment.
		panic("web assets missing from the binary: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Routing lives in the fragment, so every path serves the same page and
		// a deep link survives a reload.
		if r.URL.Path != "/" && !fileExists(sub, r.URL.Path) {
			r.URL.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

func fileExists(fsys fs.FS, path string) bool {
	name := path
	for len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}
	if name == "" {
		return false
	}
	f, err := fsys.Open(name)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}
