package mcp

// Streamable HTTP MCP bridge for operator-approved, configured remote servers.
// No open proxy: endpoints and authentication headers can only come from local
// MCP server configuration, not from model-supplied tool-call arguments.
import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type remoteMCPProxy struct {
	endpoint  string
	client    *http.Client
	headers   map[string]string
	sessionID string
	nextID    int64
	mu        sync.Mutex
}

func newRemoteMCPProxy(launch externalMCPLaunch) (*remoteMCPProxy, error) {
	endpoint := launch.URL
	if endpoint == "" {
		endpoint = launch.ServerURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("invalid remote MCP URL")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	loopback := strings.EqualFold(host, "localhost") || (ip != nil && ip.IsLoopback())
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback) {
		return nil, errors.New("remote MCP must use HTTPS, except localhost")
	}
	p := &remoteMCPProxy{
		endpoint: endpoint,
		headers:  launch.Headers,
		client:   &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	result, err := p.callRaw("initialize", map[string]interface{}{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]interface{}{},
		"clientInfo":      map[string]string{"name": "a2m-capability-bridge", "version": "1.3.0"},
	}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion == "" {
		return nil, errors.New("remote MCP handshake invalid")
	}
	p.notifyInitialized()
	return p, nil
}

func (p *remoteMCPProxy) headersFor(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2025-06-18")
	for name, value := range p.headers {
		if strings.EqualFold(name, "Host") || strings.EqualFold(name, "Content-Length") ||
			strings.EqualFold(name, "MCP-Session-Id") || strings.EqualFold(name, "MCP-Protocol-Version") ||
			strings.EqualFold(name, "Content-Type") || strings.EqualFold(name, "Accept") {
			continue
		}
		req.Header.Set(name, value)
	}
	if p.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", p.sessionID)
	}
}

func (p *remoteMCPProxy) notifyInitialized() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	body, _ := json.Marshal(map[string]string{"jsonrpc": "2.0", "method": "notifications/initialized"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	p.mu.Lock()
	p.headersFor(req)
	resp, err := p.client.Do(req)
	p.mu.Unlock()
	if err == nil {
		_ = resp.Body.Close()
	}
}

func (p *remoteMCPProxy) callRaw(method string, params interface{}, timeout time.Duration) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextID++
	id := p.nextID
	data, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid remote MCP request")
	}
	p.headersFor(request)
	response, err := p.client.Do(request)
	if err != nil {
		return nil, errors.New("remote MCP unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote MCP unavailable (HTTP %d)", response.StatusCode)
	}
	if session := response.Header.Get("Mcp-Session-Id"); session != "" && len(session) <= 256 {
		p.sessionID = session
	}
	var raw json.RawMessage
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		raw, err = readMCPStreamResponse(response.Body, id)
	} else {
		raw, err = io.ReadAll(io.LimitReader(response.Body, 8*1024*1024+1))
		if len(raw) > 8*1024*1024 {
			return nil, errors.New("remote MCP response too large")
		}
	}
	if err != nil {
		return nil, errors.New("invalid remote MCP response")
	}
	var reply graphReply
	if json.Unmarshal(raw, &reply) != nil || reply.ID != id {
		return nil, errors.New("remote MCP response id mismatch")
	}
	if reply.Error != nil {
		return nil, fmt.Errorf("remote MCP error: %s", reply.Error.Message)
	}
	if len(reply.Result) == 0 {
		return nil, errors.New("remote MCP missing result")
	}
	return reply.Result, nil
}

func readMCPStreamResponse(body io.Reader, expectedID int64) (json.RawMessage, error) {
	scanner := bufio.NewScanner(io.LimitReader(body, 8*1024*1024+1))
	scanner.Buffer(make([]byte, 4096), 8*1024*1024)
	var data strings.Builder
	tryFrame := func() (json.RawMessage, bool) {
		var result graphReply
		raw := []byte(data.String())
		data.Reset()
		if len(raw) > 0 && json.Unmarshal(raw, &result) == nil && result.ID == expectedID {
			return raw, true
		}
		return nil, false
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), string(rune(13)))
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte(10)
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		} else if line == "" {
			if raw, ok := tryFrame(); ok {
				return raw, nil
			}
		}
	}
	if raw, ok := tryFrame(); ok {
		return raw, nil
	}
	return nil, errors.New("missing MCP SSE result")
}

func (p *remoteMCPProxy) listTools() ([]ToolDefinition, error) {
	var all []ToolDefinition
	seenCursors := map[string]bool{}
	cursor := ""
	for page := 0; page < 32; page++ {
		params := map[string]interface{}{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := p.callRaw("tools/list", params, 8*time.Second)
		if err != nil {
			return nil, err
		}
		var listing struct {
			Tools      []ToolDefinition
			NextCursor string
		}
		if err := json.Unmarshal(raw, &listing); err != nil {
			return nil, err
		}
		if len(all)+len(listing.Tools) > 2048 {
			return nil, fmt.Errorf("MCP provider tool list exceeds 2048 tools")
		}
		all = append(all, listing.Tools...)
		if listing.NextCursor == "" {
			return all, nil
		}
		if seenCursors[listing.NextCursor] {
			return nil, fmt.Errorf("MCP provider repeated tools/list cursor")
		}
		seenCursors[listing.NextCursor] = true
		cursor = listing.NextCursor
	}
	return nil, fmt.Errorf("MCP provider tools/list exceeded 32 pages")
}

func (p *remoteMCPProxy) callExternalTool(name string, raw json.RawMessage) (ToolCallResult, error) {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("{}")
	}
	var args map[string]interface{}
	if json.Unmarshal(raw, &args) != nil || args == nil {
		return ErrorResult("invalid MCP call arguments"), nil
	}
	result, err := p.callRaw("tools/call", map[string]interface{}{"name": name, "arguments": args}, 60*time.Second)
	if err != nil {
		return ErrorResult("remote MCP call failed: " + err.Error()), nil
	}
	var res ToolCallResult
	if json.Unmarshal(result, &res) != nil {
		return ErrorResult("invalid remote MCP tool result"), nil
	}
	return res, nil
}
