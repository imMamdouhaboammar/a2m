package process

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ProcessState string

const (
	StateRunning         ProcessState = "running"
	StateWaitingForInput ProcessState = "waiting_for_input"
	StateCompleted       ProcessState = "completed"
	StateTerminated      ProcessState = "terminated"
	StateFailed          ProcessState = "failed"
)

const (
	MaxBufferedLines   = 50000
	MaxWaitOutputChars = 2 * 1024 * 1024
	DefaultTimeoutMs   = 30000
	MaxProcessWaitMs   = 240000
)

type OutputLine struct {
	Index     int
	Timestamp time.Time
	Stream    string // "stdout" or "stderr"
	Text      string
}

type Session struct {
	PID           int
	Command       string
	Shell         string
	State         ProcessState
	ExitCode      *int
	StartTime     time.Time
	EndTime       *time.Time
	Cmd           *exec.Cmd
	StdinPipe     io.WriteCloser
	Lines         []OutputLine
	BufferedChars int
	ReadWatermark int
	PGID          int
	Mu            sync.RWMutex
}

type OutputBroadcaster func(pid int, cmd string, stream string, text string)

type Supervisor struct {
	sessions         map[int]*Session
	blockedCommands  map[string]bool
	broadcaster      OutputBroadcaster
	broadcasters     map[uint64]OutputBroadcaster
	nextSubscriberID uint64
	mu               sync.RWMutex
}

func NewSupervisor() *Supervisor {
	return &Supervisor{
		sessions:     make(map[int]*Session),
		broadcasters: make(map[uint64]OutputBroadcaster),
		blockedCommands: map[string]bool{
			"rm -rf /":      true,
			":(){ :|:& };:": true,
		},
	}
}

func (s *Supervisor) SetBroadcaster(b OutputBroadcaster) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcaster = b
}

// AddBroadcaster subscribes a viewer without replacing the command-line hub's broadcaster.
// The returned function safely removes only this subscriber.
func (s *Supervisor) AddBroadcaster(b OutputBroadcaster) func() {
	s.mu.Lock()
	s.nextSubscriberID++
	id := s.nextSubscriberID
	s.broadcasters[id] = b
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.broadcasters, id)
		s.mu.Unlock()
	}
}

func (s *Supervisor) SetBlockedCommands(cmds []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blockedCommands = make(map[string]bool)
	for _, c := range cmds {
		s.blockedCommands[strings.ToLower(strings.TrimSpace(c))] = true
	}
}

func ExtractCommands(commandString string) []string {
	var commands []string
	separators := []string{";", "&&", "||", "|", "&", "\n", "\r\n"}

	inQuote := false
	var quoteChar rune
	var currentCmd strings.Builder
	runes := []rune(commandString)
	escaped := false

	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		if ch == '\\' && !escaped {
			escaped = true
			currentCmd.WriteRune(ch)
			continue
		}
		if escaped {
			escaped = false
			currentCmd.WriteRune(ch)
			continue
		}

		if (ch == '"' || ch == '\'') && !inQuote {
			inQuote = true
			quoteChar = ch
			currentCmd.WriteRune(ch)
			continue
		} else if inQuote && ch == quoteChar {
			inQuote = false
			quoteChar = 0
			currentCmd.WriteRune(ch)
			continue
		}

		if ch == '$' && i+1 < len(runes) && runes[i+1] == '(' {
			openParens := 1
			j := i + 2
			for j < len(runes) && openParens > 0 {
				if runes[j] == '(' {
					openParens++
				} else if runes[j] == ')' {
					openParens--
				}
				j++
			}
			if openParens == 0 {
				subContent := string(runes[i+2 : j-1])
				subCmds := ExtractCommands(subContent)
				commands = append(commands, subCmds...)
				i = j - 1
				continue
			}
		}

		if ch == '`' {
			j := i + 1
			for j < len(runes) && runes[j] != '`' {
				j++
			}
			if j < len(runes) {
				subContent := string(runes[i+1 : j])
				subCmds := ExtractCommands(subContent)
				commands = append(commands, subCmds...)
				i = j
				continue
			}
		}

		if !inQuote {
			matchedSep := false
			for _, sep := range separators {
				sepRunes := []rune(sep)
				if i+len(sepRunes) <= len(runes) && string(runes[i:i+len(sepRunes)]) == sep {
					cmdStr := strings.TrimSpace(currentCmd.String())
					if cmdStr != "" {
						commands = append(commands, cmdStr)
					}
					currentCmd.Reset()
					i += len(sepRunes) - 1
					matchedSep = true
					break
				}
			}
			if matchedSep {
				continue
			}
		}

		currentCmd.WriteRune(ch)
	}

	remaining := strings.TrimSpace(currentCmd.String())
	if remaining != "" {
		commands = append(commands, remaining)
	}

	return commands
}

func isDestructiveRm(parts []string) bool {
	if len(parts) < 2 {
		return false
	}
	base := strings.ToLower(filepath.Base(parts[0]))
	if base != "rm" {
		return false
	}
	hasRecursive := false
	hasRoot := false
	for _, arg := range parts[1:] {
		cleanArg := strings.Trim(arg, "\"'")
		if strings.HasPrefix(cleanArg, "-") {
			if strings.Contains(cleanArg, "r") || strings.Contains(cleanArg, "R") || cleanArg == "--recursive" {
				hasRecursive = true
			}
		} else {
			cleanPath := filepath.Clean(cleanArg)
			if cleanPath == "/" || cleanPath == "/*" ||
				strings.HasPrefix(cleanPath, "/etc") ||
				strings.HasPrefix(cleanPath, "/var") ||
				strings.HasPrefix(cleanPath, "/usr") ||
				strings.HasPrefix(cleanPath, "/bin") ||
				strings.HasPrefix(cleanPath, "/sbin") ||
				strings.HasPrefix(cleanPath, "/System") ||
				strings.HasPrefix(cleanPath, "/Library") ||
				strings.HasPrefix(cleanPath, "/private") {
				hasRoot = true
			}
		}
	}
	return hasRecursive && hasRoot
}

func (s *Supervisor) ValidateCommand(command string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.validateCommandInternal(command, 0)
}

func (s *Supervisor) validateCommandInternal(command string, depth int) error {
	if depth > 5 {
		return nil
	}

	extracted := ExtractCommands(command)
	for _, cmd := range extracted {
		parts := strings.Fields(cmd)
		if len(parts) == 0 {
			continue
		}
		rawCmd := strings.TrimSpace(cmd)
		normalizedCmd := strings.ToLower(strings.Join(parts, " "))
		baseCmd := strings.ToLower(parts[0])
		baseBin := strings.ToLower(filepath.Base(parts[0]))

		// 1. Direct blocklist check
		if s.blockedCommands[baseCmd] || s.blockedCommands[baseBin] || s.blockedCommands[normalizedCmd] || s.blockedCommands[strings.ToLower(rawCmd)] {
			return fmt.Errorf("command is blocked by security policy: %s", cmd)
		}

		// Check normalized base binary + args
		normalizedWithBaseBin := strings.ToLower(strings.Join(append([]string{baseBin}, parts[1:]...), " "))
		if s.blockedCommands[normalizedWithBaseBin] {
			return fmt.Errorf("command is blocked by security policy: %s", cmd)
		}

		// 2. Destructive rm -rf / check
		if isDestructiveRm(parts) {
			return fmt.Errorf("command is blocked by security policy: %s", cmd)
		}

		// 3. Fork bomb check
		noSpace := strings.ReplaceAll(cmd, " ", "")
		if strings.Contains(noSpace, ":(){:|:&};:") || strings.Contains(noSpace, ":(){:|:&}") {
			return fmt.Errorf("command is blocked by security policy: fork bomb detected")
		}

		// 4. Wrapper unpacking (sudo, env, nohup, xargs, etc.)
		if (baseBin == "sudo" || baseBin == "env" || baseBin == "nohup" || baseBin == "xargs") && len(parts) > 1 {
			idx := 1
			for idx < len(parts) && strings.HasPrefix(parts[idx], "-") {
				idx++
			}
			if idx < len(parts) {
				if err := s.validateCommandInternal(strings.Join(parts[idx:], " "), depth+1); err != nil {
					return err
				}
			}
		}

		// 5. Shell wrapper unpacking (sh -c '...', bash -c '...', zsh -c '...')
		if (baseBin == "sh" || baseBin == "bash" || baseBin == "zsh" || baseBin == "dash" || baseBin == "ksh") && len(parts) > 1 {
			for i, arg := range parts {
				if (arg == "-c" || arg == "-ic" || arg == "-ec") && i+1 < len(parts) {
					subCmd := strings.Join(parts[i+1:], " ")
					subCmd = strings.Trim(subCmd, "\"'")
					if err := s.validateCommandInternal(subCmd, depth+1); err != nil {
						return err
					}
					break
				}
			}
		}
	}
	return nil
}

type StartProcessResult struct {
	PID            int          `json:"pid"`
	Output         string       `json:"output"`
	ProcessState   ProcessState `json:"processState"`
	WaitCappedAtMs int          `json:"waitCappedAtMs,omitempty"`
	Command        string       `json:"command"`
}

func (s *Supervisor) StartProcess(command string, timeoutMs int, customShell string) (*StartProcessResult, error) {
	if err := s.ValidateCommand(command); err != nil {
		return nil, err
	}

	if timeoutMs <= 0 {
		timeoutMs = DefaultTimeoutMs
	}
	if timeoutMs > MaxProcessWaitMs {
		timeoutMs = MaxProcessWaitMs
	}

	shellPath, shellArgs := getShell(customShell, command)
	cmd := exec.Command(shellPath, shellArgs...)

	env := os.Environ()
	if runtime.GOOS == "windows" {
		hasWindir := false
		systemRoot := os.Getenv("SystemRoot")
		for _, e := range env {
			if strings.HasPrefix(strings.ToUpper(e), "WINDIR=") {
				hasWindir = true
				break
			}
		}
		if !hasWindir && systemRoot != "" {
			env = append(env, "WINDIR="+systemRoot)
		}
	}
	cmd.Env = env

	setProcessGroup(cmd)

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stderr pipe: %w", err)
	}
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to open stdin pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to spawn process: %w", err)
	}

	pid := cmd.Process.Pid
	pgid := getProcessGroupID(pid)

	session := &Session{
		PID:       pid,
		Command:   command,
		Shell:     shellPath,
		State:     StateRunning,
		StartTime: time.Now(),
		Cmd:       cmd,
		StdinPipe: stdinPipe,
		Lines:     make([]OutputLine, 0, 1024),
		PGID:      pgid,
	}

	s.mu.Lock()
	s.sessions[pid] = session
	s.mu.Unlock()

	var initialWaitBuffer bytes.Buffer
	var bufferMu sync.Mutex
	initialWaitDone := make(chan struct{})

	var streamWg sync.WaitGroup
	streamWg.Add(2)

	go s.captureStream(session, stdoutPipe, "stdout", &initialWaitBuffer, &bufferMu, &streamWg)
	go s.captureStream(session, stderrPipe, "stderr", &initialWaitBuffer, &bufferMu, &streamWg)

	go func() {
		streamWg.Wait()
		waitErr := cmd.Wait()
		now := time.Now()

		session.Mu.Lock()
		defer session.Mu.Unlock()

		session.EndTime = &now
		if waitErr != nil {
			if exitErr, ok := waitErr.(*exec.ExitError); ok {
				code := exitErr.ExitCode()
				session.ExitCode = &code
			} else {
				code := 1
				session.ExitCode = &code
			}
			if session.State != StateTerminated {
				session.State = StateFailed
			}
		} else {
			code := 0
			session.ExitCode = &code
			session.State = StateCompleted
		}
		close(initialWaitDone)
	}()

	waitLimit := time.Duration(min(timeoutMs, 2000)) * time.Millisecond
	select {
	case <-time.After(waitLimit):
	case <-initialWaitDone:
	}

	session.Mu.Lock()
	currentState := session.State
	bufferMu.Lock()
	initOutput := initialWaitBuffer.String()
	bufferMu.Unlock()
	session.ReadWatermark = len(session.Lines)
	session.Mu.Unlock()

	return &StartProcessResult{
		PID:          pid,
		Output:       initOutput,
		ProcessState: currentState,
		Command:      command,
	}, nil
}

func (s *Supervisor) captureStream(session *Session, r io.Reader, streamName string, initBuf *bytes.Buffer, bufMu *sync.Mutex, wg *sync.WaitGroup) {
	if wg != nil {
		defer wg.Done()
	}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		text := scanner.Text()

		session.Mu.Lock()
		lineIdx := len(session.Lines) + 1
		line := OutputLine{
			Index:     lineIdx,
			Timestamp: time.Now(),
			Stream:    streamName,
			Text:      text,
		}
		session.Lines = append(session.Lines, line)
		session.BufferedChars += len(text)

		if len(session.Lines) > MaxBufferedLines {
			session.Lines = session.Lines[len(session.Lines)-MaxBufferedLines:]
		}

		bufMu.Lock()
		if initBuf.Len() < MaxWaitOutputChars {
			initBuf.WriteString(text)
			initBuf.WriteString("\n")
		}
		bufMu.Unlock()

		s.mu.RLock()
		bc := s.broadcaster
		listeners := make([]OutputBroadcaster, 0, len(s.broadcasters))
		for _, subscriber := range s.broadcasters {
			listeners = append(listeners, subscriber)
		}
		s.mu.RUnlock()
		if bc != nil {
			bc(session.PID, session.Command, streamName, text)
		}
		for _, subscriber := range listeners {
			subscriber(session.PID, session.Command, streamName, text)
		}

		session.Mu.Unlock()
	}
}

type ReadOutputResult struct {
	PID          int          `json:"pid"`
	Output       string       `json:"output"`
	TotalLines   int          `json:"totalLines"`
	NextOffset   int          `json:"nextOffset"`
	HasMore      bool         `json:"hasMore"`
	ProcessState ProcessState `json:"processState"`
	ExitCode     *int         `json:"exitCode,omitempty"`
}

func (s *Supervisor) ReadProcessOutput(pid int, offset int, length int) (*ReadOutputResult, error) {
	s.mu.RLock()
	session, exists := s.sessions[pid]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("session with PID %d not found", pid)
	}

	if length <= 0 {
		length = 1000
	}

	session.Mu.Lock()
	defer session.Mu.Unlock()

	total := len(session.Lines)
	var startIdx int

	if offset == 0 {
		startIdx = session.ReadWatermark
	} else if offset > 0 {
		startIdx = offset
	} else {
		startIdx = total + offset
		if startIdx < 0 {
			startIdx = 0
		}
	}

	if startIdx > total {
		startIdx = total
	}

	endIdx := startIdx + length
	if endIdx > total {
		endIdx = total
	}

	var sb strings.Builder
	for i := startIdx; i < endIdx; i++ {
		sb.WriteString(session.Lines[i].Text)
		sb.WriteString("\n")
	}

	session.ReadWatermark = endIdx
	hasMore := endIdx < total || session.State == StateRunning

	return &ReadOutputResult{
		PID:          pid,
		Output:       sb.String(),
		TotalLines:   total,
		NextOffset:   endIdx,
		HasMore:      hasMore,
		ProcessState: session.State,
		ExitCode:     session.ExitCode,
	}, nil
}

func (s *Supervisor) InteractWithProcess(pid int, input string, timeoutMs int) (*ReadOutputResult, error) {
	s.mu.RLock()
	session, exists := s.sessions[pid]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("session with PID %d not found", pid)
	}

	session.Mu.Lock()
	if session.State != StateRunning {
		session.Mu.Unlock()
		return nil, fmt.Errorf("process %d is not running (state: %s)", pid, session.State)
	}

	if !strings.HasSuffix(input, "\n") {
		input += "\n"
	}

	_, err := io.WriteString(session.StdinPipe, input)
	session.Mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("failed to write to process stdin: %w", err)
	}

	waitDur := time.Duration(min(timeoutMs, 2000)) * time.Millisecond
	if waitDur <= 0 {
		waitDur = 500 * time.Millisecond
	}
	time.Sleep(waitDur)

	return s.ReadProcessOutput(pid, 0, 1000)
}

func (s *Supervisor) TerminateProcess(pid int, force bool) error {
	s.mu.RLock()
	session, exists := s.sessions[pid]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("session with PID %d not found", pid)
	}

	session.Mu.Lock()
	defer session.Mu.Unlock()

	if session.State == StateCompleted || session.State == StateTerminated {
		return nil
	}

	_ = killProcessGroup(pid, session.PGID, force)

	session.State = StateTerminated
	now := time.Now()
	session.EndTime = &now
	return nil
}

type SessionSummary struct {
	Source    string       `json:"source,omitempty"`
	PID       int          `json:"pid"`
	Command   string       `json:"command"`
	State     ProcessState `json:"state"`
	ExitCode  *int         `json:"exitCode,omitempty"`
	StartedAt string       `json:"startedAt"`
	RuntimeMs int64        `json:"runtimeMs"`
}

func (s *Supervisor) ListSessions() []SessionSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list := make([]SessionSummary, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sess.Mu.RLock()
		end := time.Now()
		if sess.EndTime != nil {
			end = *sess.EndTime
		}
		runtimeMs := end.Sub(sess.StartTime).Milliseconds()

		list = append(list, SessionSummary{
			PID:       sess.PID,
			Command:   sess.Command,
			State:     sess.State,
			ExitCode:  sess.ExitCode,
			StartedAt: sess.StartTime.Format(time.RFC3339),
			RuntimeMs: runtimeMs,
		})
		sess.Mu.RUnlock()
	}
	return list
}

func (s *Supervisor) ListProcesses() ([]map[string]interface{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", "Get-Process | Select-Object Id, ProcessName, CPU, WorkingSet | ConvertTo-Json")
	} else {
		cmd = exec.CommandContext(ctx, "ps", "-eo", "pid,ppid,%cpu,%mem,comm")
	}

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to query processes: %w", err)
	}

	var results []map[string]interface{}
	lines := strings.Split(string(out), "\n")
	for i, l := range lines {
		if i == 0 || strings.TrimSpace(l) == "" {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) >= 5 {
			pid, _ := strconv.Atoi(fields[0])
			results = append(results, map[string]interface{}{
				"pid":     pid,
				"ppid":    fields[1],
				"cpu":     fields[2],
				"mem":     fields[3],
				"command": strings.Join(fields[4:], " "),
			})
		}
	}

	return results, nil
}

func getShell(customShell, command string) (string, []string) {
	if customShell != "" {
		return customShell, []string{"-c", command}
	}

	if runtime.GOOS == "windows" {
		if psh, err := exec.LookPath("powershell.exe"); err == nil {
			return psh, []string{"-NoProfile", "-Command", command}
		}
		return "cmd.exe", []string{"/c", command}
	}

	if zsh, err := exec.LookPath("zsh"); err == nil {
		return zsh, []string{"-c", command}
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		return bash, []string{"-c", command}
	}
	return "/bin/sh", []string{"-c", command}
}
