package docker

// Unit coverage for the S21 MCP dispatch on the proxy listener: origin-form /mcp/* is
// served by the mounted handler, absolute-form /mcp/* is served only when addressed to one
// of our own names, and everything else keeps hitting the 407 auth gate.

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
)

// net_Dial dials the proxy's listener for raw absolute-form requests.
func net_Dial(t *testing.T, addr string) (net.Conn, error) {
	t.Helper()
	return net.Dial("tcp", addr)
}

func startMCPProxy(t *testing.T, handler http.Handler) *Proxy {
	t.Helper()
	p := NewProxy(ProxyOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	p.SetMCPHandler(handler)
	if err := p.Start("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Stop(context.Background()) })
	return p
}

func TestProxyServesMCPPaths(t *testing.T) {
	served := 0
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		w.WriteHeader(http.StatusTeapot) // distinctive: proves this handler answered
	})
	p := startMCPProxy(t, h)
	origin := "http://" + p.ln.Addr().String()

	post := func(target string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, target,
			strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}

	// Origin-form: what a container sends after dialing the relay directly.
	if resp := post(origin + "/mcp/some-token"); resp.StatusCode != http.StatusTeapot {
		t.Fatalf("origin-form /mcp = %d, want the MCP handler's answer", resp.StatusCode)
	}

	// Absolute-form addressed to one of our names: what an HTTP_PROXY client sends. Sent
	// by hand because net/http cannot be asked to proxy to itself here.
	conn, err := net_Dial(t, p.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, _ = io.WriteString(conn,
		"POST http://lexicode-egress:3128/mcp/some-token HTTP/1.1\r\n"+
			"Host: lexicode-egress:3128\r\nContent-Length: 2\r\n\r\n{}")
	buf := make([]byte, 1024)
	n, _ := conn.Read(buf)
	if !strings.Contains(string(buf[:n]), "418") {
		t.Fatalf("absolute-form /mcp to lexicode-egress = %q, want the MCP handler's 418", string(buf[:n]))
	}

	// Absolute-form /mcp on an unrelated host is NOT ours: it stays a proxied request and
	// hits the 407 gate (no credential given).
	conn2, err := net_Dial(t, p.ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn2.Close() }()
	_, _ = io.WriteString(conn2,
		"POST http://example.com/mcp/steal HTTP/1.1\r\nHost: example.com\r\nContent-Length: 0\r\n\r\n")
	n, _ = conn2.Read(buf)
	if !strings.Contains(string(buf[:n]), "407") {
		t.Fatalf("absolute-form /mcp to example.com = %q, want 407", string(buf[:n]))
	}

	// Non-/mcp origin-form requests still refuse as before (the 407 gate, auth first).
	if resp := post(origin + "/other"); resp.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("origin-form non-mcp = %d, want 407", resp.StatusCode)
	}
	if served != 2 {
		t.Fatalf("MCP handler served %d requests, want 2", served)
	}
}
