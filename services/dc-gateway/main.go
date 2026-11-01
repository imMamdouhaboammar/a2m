package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/desktop-commander/dc-gateway/auth"
	"github.com/desktop-commander/dc-gateway/config"
	"github.com/desktop-commander/dc-gateway/ipc"
	"github.com/desktop-commander/dc-gateway/mcp"
	"github.com/desktop-commander/dc-gateway/multiplex"
	"github.com/desktop-commander/dc-gateway/process"
)

const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "io.github.a2m/a2m-mcp"
	ServerVersion   = "1.0.0"
)

func getSessionSocketPath() string {
	if sock := os.Getenv("A2M_SESSION_SOCKET"); sock != "" {
		return sock
	}
	path := "/tmp/a2m-session.sock"
	if _, err := os.Stat(path); err != nil {
		if _, errLegacy := os.Stat("/tmp/desktop-commander-session.sock"); errLegacy == nil {
			return "/tmp/desktop-commander-session.sock"
		}
	}
	return path
}

func watchCapabilityShutdown(registry *mcp.Registry) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		registry.Close()
		os.Exit(0)
	}()
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "connect":
			if err := multiplex.RunConnect("http://127.0.0.1:8765"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "disconnect":
			killAll := false
			if len(os.Args) > 2 && os.Args[2] == "--all" {
				killAll = true
			}
			if err := multiplex.RunDisconnect("http://127.0.0.1:8765", killAll); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "status":
			if err := multiplex.RunStatus("http://127.0.0.1:8765"); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "sessions", "ps":
			sessionSock := getSessionSocketPath()
			if err := multiplex.RunPs(sessionSock); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "watch", "logs":
			sessionSock := getSessionSocketPath()
			if err := multiplex.RunWatch(sessionSock); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "attach":
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Usage: a2m attach <PID>\n")
				os.Exit(1)
			}
			pid, err := strconv.Atoi(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid PID: %s\n", os.Args[2])
				os.Exit(1)
			}
			sessionSock := getSessionSocketPath()
			if err := multiplex.RunAttach(sessionSock, pid); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "kill":
			if len(os.Args) < 3 {
				fmt.Fprintf(os.Stderr, "Usage: a2m kill <PID>\n")
				os.Exit(1)
			}
			pid, err := strconv.Atoi(os.Args[2])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Invalid PID: %s\n", os.Args[2])
				os.Exit(1)
			}
			sessionSock := getSessionSocketPath()
			if err := multiplex.RunKill(sessionSock, pid); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return

		case "server":
			serverFs := flag.NewFlagSet("server", flag.ExitOnError)
			host := serverFs.String("host", "127.0.0.1", "HTTP / SSE bind address (default: 127.0.0.1 loopback)")
			port := serverFs.Int("port", 8765, "HTTP / SSE port")
			sessionSocket := serverFs.String("session-socket", "", "Local process-monitor socket (isolates test instances)")
			serverToken := serverFs.String("token", "", "Pre-shared authorization token")
			serverRole := serverFs.String("role", "admin", "Role level: admin, workspace, read_only")
			requireAuth := serverFs.Bool("require-auth", false, "Require authentication")
			_ = serverFs.Parse(os.Args[2:])

			// Security invariant: non-loopback binding MUST require non-empty token authentication
			if *host != "127.0.0.1" && *host != "localhost" {
				tokenVal := *serverToken
				if tokenVal == "" {
					tokenVal = os.Getenv("DC_AUTH_TOKEN")
				}
				if tokenVal == "" {
					fmt.Fprintf(os.Stderr, "FATAL SECURITY ERROR: Binding to non-loopback interface '%s' requires authentication. Provide --token or set DC_AUTH_TOKEN.\n", *host)
					os.Exit(1)
				}
				*requireAuth = true
			}

			cfgMgr := config.NewConfigManager("")
			supervisor := process.NewSupervisor()
			supervisor.SetBlockedCommands(cfgMgr.GetConfig().BlockedCommands)

			hub := multiplex.NewMultiplexHub(supervisor, *sessionSocket)
			if err := hub.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "Process monitor: %v\n", err)
				os.Exit(1)
			}
			defer hub.Close()

			authMgr := auth.NewAuthenticator(*serverToken, auth.Role(*serverRole), *requireAuth)
			fsClient := ipc.NewFsEngineClient("")
			defer fsClient.Close()
			registry := mcp.NewRegistry(supervisor, fsClient, cfgMgr, authMgr)
			defer registry.Close()
			watchCapabilityShutdown(registry)

			srv := multiplex.NewServer(registry, supervisor, hub, authMgr)
			addr := fmt.Sprintf("%s:%d", *host, *port)
			if err := srv.Start(addr); err != nil {
				fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	socketPath := flag.String("socket", "/tmp/desktop-commander-ipc.sock", "Path to Unix domain socket for dc-fs-engine")
	configPath := flag.String("config", "", "Path to custom config.json")
	tokenFlag := flag.String("token", "", "Pre-shared authorization token (or DC_AUTH_TOKEN)")
	roleFlag := flag.String("role", "admin", "Role level: admin, workspace, read_only (or DC_AUTH_ROLE)")
	requireAuthFlag := flag.Bool("require-auth", false, "Enforce authentication for all requests (or DC_REQUIRE_AUTH)")
	flag.Parse()

	cfgMgr := config.NewConfigManager(*configPath)
	supervisor := process.NewSupervisor()
	supervisor.SetBlockedCommands(cfgMgr.GetConfig().BlockedCommands)

	sessionSock := "/tmp/desktop-commander-session.sock"
	hub := multiplex.NewMultiplexHub(supervisor, sessionSock)
	_ = hub.Start()
	defer hub.Close()

	authMgr := auth.NewAuthenticator(*tokenFlag, auth.Role(*roleFlag), *requireAuthFlag)

	fsClient := ipc.NewFsEngineClient(*socketPath)
	defer fsClient.Close()
	registry := mcp.NewRegistry(supervisor, fsClient, cfgMgr, authMgr)
	defer registry.Close()
	watchCapabilityShutdown(registry)

	reader := bufio.NewReader(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)

	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			break
		}

		if len(line) == 0 || line[0] == '\n' || line[0] == '\r' {
			continue
		}

		var req mcp.Request
		if err := json.Unmarshal(line, &req); err != nil {
			errResp := mcp.Response{
				JsonRpc: "2.0",
				ID:      nil,
				Error: &mcp.ErrorObject{
					Code:    -32700,
					Message: fmt.Sprintf("Parse error: %v", err),
				},
			}
			sendResponse(writer, errResp)
			continue
		}

		handleRequest(&req, registry, authMgr, writer)
	}
}

func handleRequest(req *mcp.Request, registry *mcp.Registry, authMgr *auth.Authenticator, writer *bufio.Writer) {
	switch req.Method {
	case "initialize":
		var initParams struct {
			AuthToken             string `json:"authToken"`
			InitializationOptions struct {
				AuthToken string `json:"authToken"`
			} `json:"initializationOptions"`
		}
		_ = json.Unmarshal(req.Params, &initParams)
		candidateToken := initParams.AuthToken
		if candidateToken == "" {
			candidateToken = initParams.InitializationOptions.AuthToken
		}
		if candidateToken != "" {
			authMgr.Authenticate(candidateToken)
		}

		instructions := "A2M (Agent to Machine) provides high-performance tools for terminal execution, fast search, and surgical file editing under user control."
		if authMgr.IsEnabled() && !authMgr.IsAuthenticated() {
			instructions += " NOTE: Authentication required. Call 'auth_login' with your DC_AUTH_TOKEN."
		}

		resp := mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Result: mcp.InitializeResult{
				ProtocolVersion: ProtocolVersion,
				Capabilities: map[string]interface{}{
					"tools": map[string]interface{}{},
				},
				ServerInfo: mcp.ServerInfo{
					Name:    ServerName,
					Version: ServerVersion,
				},
				Instructions: instructions,
			},
		}
		sendResponse(writer, resp)

	case "notifications/initialized":
		// Acknowledgement notification - no response required

	case "tools/list":
		tools := registry.ListTools()
		resp := mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Result: map[string]interface{}{
				"tools": tools,
			},
		}
		sendResponse(writer, resp)

	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			resp := mcp.Response{
				JsonRpc: "2.0",
				ID:      req.ID,
				Error: &mcp.ErrorObject{
					Code:    -32602,
					Message: fmt.Sprintf("Invalid params: %v", err),
				},
			}
			sendResponse(writer, resp)
			return
		}

		result := registry.Dispatch(params.Name, params.Arguments)
		resp := mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Result:  result,
		}
		sendResponse(writer, resp)

	default:
		resp := mcp.Response{
			JsonRpc: "2.0",
			ID:      req.ID,
			Error: &mcp.ErrorObject{
				Code:    -32601,
				Message: fmt.Sprintf("Method not found: %s", req.Method),
			},
		}
		sendResponse(writer, resp)
	}
}

func sendResponse(w *bufio.Writer, resp mcp.Response) {
	bytes, err := json.Marshal(resp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to marshal response: %v\n", err)
		return
	}
	bytes = append(bytes, '\n')
	_, _ = w.Write(bytes)
	_ = w.Flush()
}
