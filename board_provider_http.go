package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// board_provider_http.go — reading a board from an MCP server over HTTP.
//
// The catalog has always described two kinds of server (mcp_catalog.go: "either a stdio server
// (command+args+env) or an HTTP one (url+headers)"), and the board provider client only implemented
// the first. An HTTP entry was silently skipped — no error, no board, no explanation — which is the
// worst way to not support something.
//
// It matters more than it looks: a hosted MCP server behind a bearer token is a completely ordinary
// deployment (a Cloudflare Worker, an internal service), and for those there is no subprocess to
// spawn at all. Requiring one would have meant every hosted server needed a local bridge process
// invented for partyline's benefit.

// httpTransport speaks MCP over the streamable-HTTP transport: JSON-RPC in a POST body, a response
// that is either JSON or an SSE frame, and an optional session id the server hands back on
// initialize and expects on subsequent calls.
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client
	session string
}

func newHTTPTransport(url string, headers map[string]string) *httpTransport {
	return &httpTransport{
		url:     url,
		headers: headers,
		client:  &http.Client{Timeout: providerTimeout},
	}
}

// call sends one JSON-RPC request and returns its result.
func (h *httpTransport) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		body["params"] = params
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", h.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Both, because a streamable-HTTP server may answer either way and chooses based on this.
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	if h.session != "" {
		req.Header.Set("Mcp-Session-Id", h.session)
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Carried on every later call in this exchange, for servers that keep session state.
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		h.session = s
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Named specifically because it is the failure an operator can actually fix, and because the
		// generic "provider returned 401" sends people looking at the wrong thing.
		return nil, fmt.Errorf("the server refused this machine's credentials (HTTP %d) — check the "+
			"Authorization header on its entry in ~/.partyline/mcp.json", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from the provider", resp.StatusCode)
	}

	payload, err := readMCPHTTPBody(resp)
	if err != nil {
		return nil, err
	}
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("provider returned something this board cannot read: %w", err)
	}
	if out.Error != nil {
		return nil, fmt.Errorf("%s", out.Error.Message)
	}
	return out.Result, nil
}

// readMCPHTTPBody returns the JSON-RPC document, whether the server sent it as a plain body or as
// an SSE frame.
//
// Streamable HTTP lets a server answer a single request with an event stream, and several do by
// default. Reading the body naively then fails to parse for a reason nobody can see, so the two
// shapes are handled here rather than being a mystery at the call site.
func readMCPHTTPBody(resp *http.Response) ([]byte, error) {
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	}
	sc := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// The first data: frame carrying a JSON object is the response; comments (":"), event: and
		// id: lines are framing we do not need.
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			if d := strings.TrimSpace(data); strings.HasPrefix(d, "{") {
				return []byte(d), nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("the provider's event stream carried no response")
}

// readResource performs the handshake and reads one resource's text.
func (h *httpTransport) readResource(uri string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
	defer cancel()

	if _, err := h.call(ctx, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "ptln-board", "version": version},
	}); err != nil {
		return nil, err
	}
	raw, err := h.call(ctx, "resources/read", map[string]any{"uri": uri})
	if err != nil {
		return nil, err
	}
	return resourceText(raw, uri)
}

// resourceText pulls the document out of a resources/read result. Shared by both transports so an
// HTTP server and a stdio one cannot disagree about what a valid answer looks like.
func resourceText(raw json.RawMessage, uri string) ([]byte, error) {
	var out struct {
		Contents []struct {
			Text string `json:"text"`
			Blob string `json:"blob"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("provider returned something this board cannot read: %w", err)
	}
	for _, c := range out.Contents {
		if strings.TrimSpace(c.Text) != "" {
			return []byte(c.Text), nil
		}
	}
	return nil, fmt.Errorf("provider returned an empty %s", uri)
}
