package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func testFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"index.html.gz":        {Data: []byte("gzipped index")},
		"favicon.ico":          {Data: []byte("icon")},
		"assets/app-abc123.js": {Data: []byte("console.log(1)")},
		"assets/app-abc123.js.br": {
			Data: []byte("brotli js"),
		},
		"assets/app-abc123.js.gz": {Data: []byte("gzipped js")},
	}
}

var api = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-From", "api")
	w.WriteHeader(http.StatusTeapot)
})

func get(t *testing.T, h http.Handler, method, target, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexHTMLWithoutFrontend(t *testing.T) {
	if !Built() && IndexHTML() != nil {
		t.Fatal("index.html without a built frontend")
	}
}

func TestMountWithoutFrontendReturnsAPI(t *testing.T) {
	h := mount(api, fstest.MapFS{".gitkeep": {}})
	if rec := get(t, h, "GET", "/", ""); rec.Code != http.StatusTeapot {
		t.Fatalf("status %d, want the API handler", rec.Code)
	}
}

func TestMountRoutes(t *testing.T) {
	h := mount(api, testFS())
	tests := []struct {
		name, method, target, acceptEncoding string
		wantStatus                           int
		wantBody, wantEncoding, wantCache    string
		wantAPI                              bool
	}{
		{name: "api", method: "GET", target: "/api/v1/monitors", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "unknown api route stays in api", method: "GET", target: "/api/nope", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "api post", method: "POST", target: "/api/v1/beats", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "healthz", method: "GET", target: "/healthz", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "root goes to the api, which picks the entry page", method: "GET", target: "/", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "status page", method: "GET", target: "/status/main", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "default status page", method: "GET", target: "/status", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "old status page path", method: "GET", target: "/status-page", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "uploaded logo", method: "GET", target: "/upload/logo1.png", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "screenshot", method: "GET", target: "/screenshots/3.png", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "robots.txt", method: "GET", target: "/robots.txt", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "mcp", method: "POST", target: "/mcp", wantStatus: http.StatusTeapot, wantAPI: true},
		{name: "index.html itself", method: "GET", target: "/index.html", wantStatus: 200, wantBody: "<html>app</html>"},
		{name: "spa route", method: "GET", target: "/dashboard/12", wantStatus: 200, wantBody: "<html>app</html>", wantCache: "no-cache"},
		{name: "static file", method: "GET", target: "/favicon.ico", wantStatus: 200, wantBody: "icon", wantCache: "no-cache"},
		{name: "missing file", method: "GET", target: "/missing.js", wantStatus: 404},
		{name: "hashed asset", method: "GET", target: "/assets/app-abc123.js", wantStatus: 200, wantBody: "console.log(1)", wantCache: "public, max-age=31536000, immutable"},
		{name: "brotli preferred", method: "GET", target: "/assets/app-abc123.js", acceptEncoding: "gzip, br", wantStatus: 200, wantBody: "brotli js", wantEncoding: "br"},
		{name: "gzip", method: "GET", target: "/assets/app-abc123.js", acceptEncoding: "gzip", wantStatus: 200, wantBody: "gzipped js", wantEncoding: "gzip"},
		{name: "refused encoding", method: "GET", target: "/assets/app-abc123.js", acceptEncoding: "br;q=0, gzip", wantStatus: 200, wantBody: "gzipped js", wantEncoding: "gzip"},
		{name: "spa route compressed", method: "GET", target: "/settings", acceptEncoding: "gzip", wantStatus: 200, wantBody: "gzipped index", wantEncoding: "gzip"},
		{name: "path traversal is redirected to the clean path", method: "GET", target: "/../../etc/passwd", wantStatus: http.StatusTemporaryRedirect},
		{name: "outside path is an app route", method: "GET", target: "/etc/passwd", wantStatus: 200, wantBody: "<html>app</html>"},
		{name: "post to ui", method: "POST", target: "/settings", wantStatus: http.StatusMethodNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, h, tt.method, tt.target, tt.acceptEncoding)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status %d, want %d", rec.Code, tt.wantStatus)
			}
			if got := rec.Header().Get("X-From") == "api"; got != tt.wantAPI {
				t.Fatalf("served by api: %v, want %v", got, tt.wantAPI)
			}
			if tt.wantBody != "" && rec.Body.String() != tt.wantBody {
				t.Errorf("body %q, want %q", rec.Body.String(), tt.wantBody)
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.wantEncoding {
				t.Errorf("Content-Encoding %q, want %q", got, tt.wantEncoding)
			}
			if tt.wantCache != "" && rec.Header().Get("Cache-Control") != tt.wantCache {
				t.Errorf("Cache-Control %q, want %q", rec.Header().Get("Cache-Control"), tt.wantCache)
			}
		})
	}
}

func TestContentTypeOfCompressedFile(t *testing.T) {
	rec := get(t, mount(api, testFS()), "GET", "/assets/app-abc123.js", "br")
	if got := rec.Header().Get("Content-Type"); got != "text/javascript; charset=utf-8" {
		t.Errorf("Content-Type %q, want JavaScript", got)
	}
}
