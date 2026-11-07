package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
)

type withKey struct{ key string }

func (k withKey) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if k.key != "" {
		r.Header.Set("Authorization", "Bearer "+k.key)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	// The API answers with what it was asked, and refuses the "limited" key, as a missing
	// scope would.
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer limited" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"this credential doesn't have the monitors:read scope"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery, "auth": r.Header.Get("Authorization")})
	})
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if a := r.Header.Get("Authorization"); a != "Bearer good" && a != "Bearer limited" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	handler, err := Handler(Options{API: stub, OpenAPI: api.OpenAPI(), Version: "1.2.3", Authenticate: authenticate})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts
}

func connect(t *testing.T, url, key string) (*mcp.ClientSession, error) {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	return client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: url, HTTPClient: &http.Client{Transport: withKey{key}},
	}, nil)
}

func text(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("content: %+v", r.Content)
	}
	return r.Content[0].(*mcp.TextContent).Text
}

func TestMCP(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, err := connect(t, ts.URL, ""); err == nil {
		t.Fatal("connected without a key")
	}
	session, err := connect(t, ts.URL, "good")
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if info := session.InitializeResult().ServerInfo; info.Name != "uptime-kuma" || info.Version != "1.2.3" {
		t.Fatalf("server info: %+v", info)
	}

	list, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Errorf("%s isn't marked read-only", tool.Name)
		}
	}
	for _, want := range []string{"getBeats", "getMonitor", "getStats", "listMonitors", "listNotifications"} {
		if !slices.Contains(names, want) {
			t.Errorf("no %s tool in %v", want, names)
		}
	}
	for _, unwanted := range []string{"streamEvents", "getOpenApi", "health", "createMonitor"} {
		if slices.Contains(names, unwanted) {
			t.Errorf("%s is a tool", unwanted)
		}
	}

	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return r
	}
	r := call("getMonitor", map[string]any{"id": 5})
	if r.IsError || !strings.Contains(text(t, r), `"path": "/api/v1/monitors/5"`) || !strings.Contains(text(t, r), `"auth": "Bearer good"`) {
		t.Fatalf("getMonitor: %s", text(t, r))
	}
	r = call("listMonitors", map[string]any{"limit": 2, "active": true, "type": "http"})
	if r.IsError || !strings.Contains(text(t, r), `"query": "active=true\u0026limit=2\u0026type=http"`) {
		t.Fatalf("listMonitors: %s", text(t, r))
	}
	if r := call("getStats", nil); r.IsError || !strings.Contains(text(t, r), "/api/v1/stats") {
		t.Fatalf("getStats: %s", text(t, r))
	}
	// The SDK checks the arguments against the schema before the call is made.
	if r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "getMonitor", Arguments: map[string]any{}}); err == nil && !r.IsError {
		t.Fatalf("no id: %s", text(t, r))
	}
	if r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "getMonitor", Arguments: map[string]any{"id": 1, "x": 1}}); err == nil && !r.IsError {
		t.Fatalf("unknown argument: %s", text(t, r))
	}

	limited, err := connect(t, ts.URL, "limited")
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	r, err = limited.CallTool(ctx, &mcp.CallToolParams{Name: "getMonitor", Arguments: map[string]any{"id": 1}})
	if err != nil || !r.IsError || !strings.Contains(text(t, r), "HTTP 403") {
		t.Fatalf("without the scope: %+v %v", r, err)
	}
}

func TestTarget(t *testing.T) {
	tools, err := buildTools(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	var beats tool
	for _, tool := range tools {
		if tool.def.Name == "getBeats" {
			beats = tool
		}
	}
	for args, want := range map[string]string{
		`{"id": 3}`:               "/api/v1/monitors/3/beats",
		`{"id": "3", "limit": 5}`: "/api/v1/monitors/3/beats?limit=5",
		`{"id": "a/b"}`:           "/api/v1/monitors/a%2Fb/beats",
	} {
		if got, err := beats.target(json.RawMessage(args)); err != nil || got != want {
			t.Errorf("%s: %q %v", args, got, err)
		}
	}
	for _, bad := range []string{`{}`, `{"id": 1, "x": 2}`, `{"id": {"a": 1}}`, `[1]`} {
		if _, err := beats.target(json.RawMessage(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}
