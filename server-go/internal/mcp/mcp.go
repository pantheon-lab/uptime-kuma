// Package mcp serves the Model Context Protocol at /mcp, so that AI assistants can read
// monitors, beats and notifications.
//
// Its tools are the REST API's read-only operations, built from the API's OpenAPI document,
// as the Node.js backend built them from its routes. A tool call is made to the REST API
// in-process with the caller's own key, so the key's scopes and rate limit apply as they do to
// the API itself, and the answers are the API's.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// excluded operations aren't tools: a stream never ends, and the document and health check
// say nothing about monitors.
var excluded = []string{"streamEvents", "getOpenApi", "health"}

// maxResult bounds a tool's answer, which goes into an assistant's context.
const maxResult = 1 << 20

type parameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description"`
	Schema      map[string]any `json:"schema"`
}

type operation struct {
	OperationID string      `json:"operationId"`
	Summary     string      `json:"summary"`
	Description string      `json:"description"`
	Parameters  []parameter `json:"parameters"`
}

type tool struct {
	def  *mcp.Tool
	path string
	in   map[string]parameter
}

// Options configures the handler.
type Options struct {
	// API serves the REST API.
	API http.Handler
	// OpenAPI is the API's document, which the tools come from.
	OpenAPI []byte
	// Version is the server's version, which clients are told.
	Version string
	// Authenticate lets a request through only with a valid API key or the admin token.
	Authenticate func(http.Handler) http.Handler
}

// Handler returns the MCP endpoint.
func Handler(opts Options) (http.Handler, error) {
	tools, err := buildTools(opts.OpenAPI)
	if err != nil {
		return nil, err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "uptime-kuma", Version: opts.Version}, nil)
	for _, t := range tools {
		server.AddTool(t.def, t.handler(opts.API))
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		// Nothing is kept between requests, so that any instance can answer.
		Stateless:    true,
		JSONResponse: true,
		// The protection is against a web page reaching a server on localhost through DNS
		// rebinding. A page can't send the bearer key that every request needs, and a reverse
		// proxy on the same host sends its own Host, which the protection would refuse.
		DisableLocalhostProtection: true,
	})
	return opts.Authenticate(handler), nil
}

func buildTools(document []byte) ([]tool, error) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(document, &doc); err != nil {
		return nil, fmt.Errorf("read the OpenAPI document: %w", err)
	}
	var tools []tool
	for path, methods := range doc.Paths {
		raw, ok := methods["get"]
		if !ok {
			continue
		}
		var op operation
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		if op.OperationID == "" || slices.Contains(excluded, op.OperationID) {
			continue
		}
		properties := map[string]any{}
		required := []string{}
		in := map[string]parameter{}
		for _, p := range op.Parameters {
			if p.Name == "" || (p.In != "path" && p.In != "query") {
				continue
			}
			schema := map[string]any{}
			for k, v := range p.Schema {
				schema[k] = v
			}
			if p.Description != "" {
				schema["description"] = p.Description
			}
			properties[p.Name] = schema
			if p.Required || p.In == "path" {
				required = append(required, p.Name)
			}
			in[p.Name] = p
		}
		sort.Strings(required)
		description := op.Summary
		if op.Description != "" {
			description = strings.TrimSpace(op.Summary + ". " + op.Description)
		}
		tools = append(tools, tool{
			def: &mcp.Tool{
				Name:        op.OperationID,
				Description: description,
				InputSchema: map[string]any{
					"type": "object", "properties": properties, "required": required, "additionalProperties": false,
				},
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
			},
			path: path,
			in:   in,
		})
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].def.Name < tools[j].def.Name })
	return tools, nil
}

func (t tool) handler(api http.Handler) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		target, err := t.target(req.Params.Arguments)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		call, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if req.Extra != nil {
			call.Header.Set("Authorization", req.Extra.Header.Get("Authorization"))
		}
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, call)
		body, _ := io.ReadAll(io.LimitReader(recorder.Body, maxResult+1))
		if len(body) > maxResult {
			return errorResult("the answer is too large; ask for fewer items"), nil
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			body = pretty.Bytes()
		}
		if recorder.Code >= 300 {
			return errorResult(fmt.Sprintf("HTTP %d: %s", recorder.Code, body)), nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(body)}}}, nil
	}
}

// target is the API address for a call's arguments.
func (t tool) target(raw json.RawMessage) (string, error) {
	args := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", errors.New("the arguments must be a JSON object")
		}
	}
	path := t.path
	query := url.Values{}
	for name, value := range args {
		p, ok := t.in[name]
		if !ok {
			return "", fmt.Errorf("unknown argument %q", name)
		}
		var text string
		switch v := value.(type) {
		case string:
			text = v
		case float64, bool:
			text = fmt.Sprint(v)
		default:
			return "", fmt.Errorf("argument %q must be a string, number or boolean", name)
		}
		if p.In == "path" {
			path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(text))
		} else {
			query.Set(name, text)
		}
	}
	for name, p := range t.in {
		if p.In == "path" && strings.Contains(path, "{"+name+"}") {
			return "", fmt.Errorf("missing argument %q", name)
		}
		if p.Required && p.In == "query" && !query.Has(name) {
			return "", fmt.Errorf("missing argument %q", name)
		}
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path, nil
}

func errorResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}
