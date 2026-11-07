// Package webui serves the Vue frontend from src/, built by Vite and embedded in the binary.
//
// The Docker build copies the frontend's dist/ into this package's dist/ before compiling. A plain
// `go build` embeds only dist/.gitkeep, and then Mount serves the API alone.
package webui

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
)

//go:embed all:dist
var embedded embed.FS

// Built reports whether the binary holds a built frontend.
func Built() bool {
	return hasIndex(mustSub(embedded))
}

// IndexHTML returns the frontend's index.html, or nil without a built frontend.
func IndexHTML() []byte {
	data, err := fs.ReadFile(mustSub(embedded), "index.html")
	if err != nil {
		return nil
	}
	return data
}

// Mount returns a handler that sends /api/, /healthz, status pages, uploaded logos and / itself
// to api, and everything else to the frontend. Without a built frontend it returns api unchanged.
func Mount(api http.Handler) http.Handler {
	return mount(api, mustSub(embedded))
}

func mustSub(fsys embed.FS) fs.FS {
	sub, err := fs.Sub(fsys, "dist")
	if err != nil {
		panic(err) // "dist" is a valid path, so this can't happen.
	}
	return sub
}

func hasIndex(fsys fs.FS) bool {
	info, err := fs.Stat(fsys, "index.html")
	return err == nil && !info.IsDir()
}

func mount(api http.Handler, fsys fs.FS) http.Handler {
	if !hasIndex(fsys) {
		return api
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/healthz", api)
	// Status pages are rendered with their data, / decides between a custom domain's status
	// page and the entry page, and screenshots come from the database.
	for _, pattern := range []string{"/status", "/status/", "/status-page", "/upload/", "/screenshots/", "GET /{$}"} {
		mux.Handle(pattern, api)
	}
	mux.Handle("/", &files{fsys: fsys})
	return mux
}

type files struct {
	fsys fs.FS
}

// The encodings Vite's compression plugin writes next to each file, in order of preference.
var encodings = []struct{ name, ext string }{
	{"br", ".br"},
	{"gzip", ".gz"},
}

func (f *files) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if name == "" {
		name = "index.html"
	}
	if !f.isFile(name) {
		// A missing file with an extension is a real 404. Anything else is a route of the
		// single-page app, which index.html handles in the browser.
		if path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		name = "index.html"
	}

	header := w.Header()
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	if strings.HasPrefix(name, "assets/") {
		// Vite puts a content hash in every file name under assets/.
		header.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		header.Set("Cache-Control", "no-cache")
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		header.Set("Content-Type", contentType)
	}

	served := name
	header.Add("Vary", "Accept-Encoding")
	accept := r.Header.Get("Accept-Encoding")
	for _, enc := range encodings {
		if acceptsEncoding(accept, enc.name) && f.isFile(name+enc.ext) {
			header.Set("Content-Encoding", enc.name)
			served = name + enc.ext
			break
		}
	}

	file, err := f.fsys.Open(served)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	defer func() { _ = file.Close() }()
	content, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Embedded files have no modification time, so caching relies on Cache-Control.
	http.ServeContent(w, r, name, time.Time{}, content)
}

func (f *files) isFile(name string) bool {
	info, err := fs.Stat(f.fsys, name)
	return err == nil && !info.IsDir()
}

// acceptsEncoding reports whether an Accept-Encoding header allows enc, ignoring q=0 entries.
func acceptsEncoding(header, enc string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(token), enc) {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			key, value, _ := strings.Cut(strings.TrimSpace(param), "=")
			if strings.EqualFold(key, "q") {
				q, err := strconv.ParseFloat(value, 64)
				return err == nil && q > 0
			}
		}
		return true
	}
	return false
}
