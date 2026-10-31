package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type JsonRpcRequest struct {
	JsonRpc string      `json:"jsonrpc"`
	ID      uint64      `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type JsonRpcResponse struct {
	JsonRpc string          `json:"jsonrpc"`
	ID      *uint64         `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JsonRpcError   `json:"error,omitempty"`
}

type JsonRpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type FsEngineClient struct {
	socketPath string
	binaryPath string
	seq        uint64
	mu         sync.Mutex
	conn       net.Conn
	reader     *bufio.Reader
	ownedCmd   *exec.Cmd
}

func NewFsEngineClient(socketPath string) *FsEngineClient {
	if socketPath == "" {
		socketPath = "/tmp/desktop-commander-ipc.sock"
	}

	execPath, err := os.Executable()
	var binPath string
	if err == nil {
		binDir := filepath.Dir(execPath)
		candidate := filepath.Join(binDir, "dc-fs-engine")
		if _, err := os.Stat(candidate); err == nil {
			binPath = candidate
		}
	}
	if binPath == "" {
		candidates := []string{"./bin/dc-fs-engine", "bin/dc-fs-engine", "../bin/dc-fs-engine", "../../bin/dc-fs-engine"}
		for _, cand := range candidates {
			if _, err := os.Stat(cand); err == nil {
				if abs, err := filepath.Abs(cand); err == nil {
					binPath = abs
					break
				}
			}
		}
		if binPath == "" {
			binPath = "dc-fs-engine"
		}
	}

	return &FsEngineClient{
		socketPath: socketPath,
		binaryPath: binPath,
	}
}

func (c *FsEngineClient) ensureDaemon() error {
	network := "unix"
	addr := c.socketPath
	isTCP := strings.Contains(addr, ":") || runtime.GOOS == "windows"
	if isTCP {
		network = "tcp"
		if !strings.Contains(addr, ":") {
			addr = "127.0.0.1:58432"
		}
	}

	conn, err := net.Dial(network, addr)
	if err == nil {
		c.conn = conn
		c.reader = bufio.NewReader(conn)
		return nil
	}

	fmt.Fprintf(os.Stderr, "[dc-gateway] Starting dc-fs-engine binary at '%s' with %s '%s'\n", c.binaryPath, network, addr)
	var cmd *exec.Cmd
	if isTCP {
		cmd = exec.Command(c.binaryPath, "daemon", "--tcp", addr)
	} else {
		cmd = exec.Command(c.binaryPath, "daemon", "--socket", addr)
	}
	// Avoid piping to os.Stderr directly to prevent holding open test harness pipes
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to auto-start dc-fs-engine (%s): %w", c.binaryPath, err)
	}
	c.ownedCmd = cmd

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		conn, err := net.Dial(network, addr)
		if err == nil {
			fmt.Fprintf(os.Stderr, "[dc-gateway] Connected to dc-fs-engine %s\n", network)
			c.conn = conn
			c.reader = bufio.NewReader(conn)
			return nil
		}
	}

	return fmt.Errorf("timed out connecting to dc-fs-engine at %s (%s)", addr, network)
}

func (c *FsEngineClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
		c.reader = nil
	}

	if c.ownedCmd != nil && c.ownedCmd.Process != nil {
		_ = c.ownedCmd.Process.Kill()
		_ = c.ownedCmd.Wait()
		c.ownedCmd = nil
	}

	return nil
}

func (c *FsEngineClient) Call(method string, params interface{}) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		if err := c.ensureDaemon(); err != nil {
			return nil, err
		}
	}

	reqID := atomic.AddUint64(&c.seq, 1)
	req := JsonRpcRequest{
		JsonRpc: "2.0",
		ID:      reqID,
		Method:  method,
		Params:  params,
	}

	bytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	bytes = append(bytes, '\n')
	if _, err := c.conn.Write(bytes); err != nil {
		_ = c.conn.Close()
		c.conn = nil
		if err := c.ensureDaemon(); err != nil {
			return nil, fmt.Errorf("failed to reconnect to dc-fs-engine: %w", err)
		}
		if _, err := c.conn.Write(bytes); err != nil {
			return nil, fmt.Errorf("write error after reconnect: %w", err)
		}
	}

	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		_ = c.conn.Close()
		c.conn = nil
		return nil, fmt.Errorf("read error from dc-fs-engine: %w", err)
	}

	var resp JsonRpcResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("invalid json-rpc response: %w", err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("engine error [%d]: %s", resp.Error.Code, resp.Error.Message)
	}

	return resp.Result, nil
}
