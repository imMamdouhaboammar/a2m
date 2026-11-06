package ipc

import (
	"bufio"
	"bytes"
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
		socketPath = "/tmp/a2m-ipc.sock"
	}

	var binPath string
	if envBin := os.Getenv("A2M_FS_BINARY"); envBin != "" {
		if info, err := os.Stat(envBin); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			binPath = envBin
		}
	}

	execPath, err := os.Executable()
	names := []string{"a2m-fs", "dc-fs-engine"}
	if binPath == "" && err == nil {
		dirs := []string{filepath.Dir(execPath)}
		if real, err := filepath.EvalSymlinks(execPath); err == nil {
			realDir := filepath.Dir(real)
			if realDir != dirs[0] {
				dirs = append(dirs, realDir)
			}
		}
		for _, dir := range dirs {
			for _, name := range names {
				candidate := filepath.Join(dir, name)
				if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
					binPath = candidate
					break
				}
			}
			if binPath != "" {
				break
			}
		}
	}
	if binPath == "" {
		candidates := []string{"./bin", "bin", "../bin", "../../bin", "../../../bin"}
		for _, dir := range candidates {
			for _, name := range names {
				cand := filepath.Join(dir, name)
				if info, err := os.Stat(cand); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
					if abs, err := filepath.Abs(cand); err == nil {
						binPath = abs
						break
					}
				}
			}
			if binPath != "" {
				break
			}
		}
		if binPath == "" {
			binPath = "a2m-fs"
		}
	}

	return &FsEngineClient{
		socketPath: socketPath,
		binaryPath: binPath,
	}
}

// EnsureRunning guarantees the filesystem engine daemon is reachable,
// spawning the local binary if not already running.
func (c *FsEngineClient) EnsureRunning() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ensureDaemon()
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

	fmt.Fprintf(os.Stderr, "[dc-gateway] Starting %s binary at '%s' with %s '%s'\n", filepath.Base(c.binaryPath), c.binaryPath, network, addr)
	var cmd *exec.Cmd
	if isTCP {
		cmd = exec.Command(c.binaryPath, "daemon", "--tcp", addr)
	} else {
		cmd = exec.Command(c.binaryPath, "daemon", "--socket", addr)
	}
	var errBuf bytes.Buffer
	cmd.Stdout = nil
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to auto-start %s (%s): %w", filepath.Base(c.binaryPath), c.binaryPath, err)
	}
	c.ownedCmd = cmd

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		conn, err := net.Dial(network, addr)
		if err == nil {
			fmt.Fprintf(os.Stderr, "[dc-gateway] Connected to %s %s\n", filepath.Base(c.binaryPath), network)
			c.conn = conn
			c.reader = bufio.NewReader(conn)
			return nil
		}
	}

	errMsg := strings.TrimSpace(errBuf.String())
	if errMsg != "" {
		return fmt.Errorf("timed out connecting to %s at %s (%s): %s", filepath.Base(c.binaryPath), addr, network, errMsg)
	}
	return fmt.Errorf("timed out connecting to %s at %s (%s)", filepath.Base(c.binaryPath), addr, network)
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
