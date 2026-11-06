package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestFsEngineClientTCPCall(t *testing.T) {
	// Start mock TCP JSON-RPC server on ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on tcp: %v", err)
	}
	defer listener.Close()

	tcpAddr := listener.Addr().String()

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}

		var req JsonRpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			return
		}

		resp := JsonRpcResponse{
			JsonRpc: "2.0",
			ID:      &req.ID,
			Result:  json.RawMessage(`{"status":"mock_tcp_ok","method":"` + req.Method + `"}`),
		}
		respBytes, _ := json.Marshal(resp)
		respBytes = append(respBytes, '\n')
		_, _ = conn.Write(respBytes)
	}()

	client := NewFsEngineClient(tcpAddr)
	res, err := client.Call("fs.test_method", map[string]string{"foo": "bar"})
	if err != nil {
		t.Fatalf("client.Call failed: %v", err)
	}

	if !strings.Contains(string(res), "mock_tcp_ok") {
		t.Errorf("expected mock_tcp_ok in result, got: %s", string(res))
	}
}

func TestFsEngineClientEnsureRunning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on tcp: %v", err)
	}
	defer listener.Close()

	tcpAddr := listener.Addr().String()
	client := NewFsEngineClient(tcpAddr)
	defer client.Close()

	if err := client.EnsureRunning(); err != nil {
		t.Fatalf("EnsureRunning failed against active listener: %v", err)
	}
	if client.conn == nil {
		t.Fatal("expected client.conn to be non-nil after EnsureRunning")
	}

	// Calling again when already connected is a no-op
	if err := client.EnsureRunning(); err != nil {
		t.Fatalf("second EnsureRunning failed: %v", err)
	}
}
