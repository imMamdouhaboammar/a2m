package process

import (
	"strings"
	"testing"
	"time"
)

func TestExtractCommandsWithNewlines(t *testing.T) {
	// Reproduce and verify fix for Issue #957:
	// extractCommands separator set omits newline — advisory blocklist misses commands after a line break
	input := "echo hi\nsudo whoami"
	commands := ExtractCommands(input)

	if len(commands) != 2 {
		t.Fatalf("expected 2 commands, got %d: %v", len(commands), commands)
	}
	if commands[0] != "echo hi" {
		t.Errorf("expected 'echo hi', got '%s'", commands[0])
	}
	if commands[1] != "sudo whoami" {
		t.Errorf("expected 'sudo whoami', got '%s'", commands[1])
	}
}

func TestExtractCommandsComplexChaining(t *testing.T) {
	input := "echo start; ls -la && grep -r 'test' | wc -l || rm -f tmp.txt; echo done"
	commands := ExtractCommands(input)

	expected := []string{
		"echo start",
		"ls -la",
		"grep -r 'test'",
		"wc -l",
		"rm -f tmp.txt",
		"echo done",
	}

	if len(commands) != len(expected) {
		t.Fatalf("expected %d commands, got %d: %v", len(expected), len(commands), commands)
	}
	for i, exp := range expected {
		if commands[i] != exp {
			t.Errorf("command %d mismatch: expected '%s', got '%s'", i, exp, commands[i])
		}
	}
}

func TestExtractCommandsSubshellRecursion(t *testing.T) {
	input := "echo $(rm -rf /) && echo `cat secret.txt`"
	commands := ExtractCommands(input)

	foundRm := false
	foundCat := false
	for _, cmd := range commands {
		if strings.Contains(cmd, "rm -rf") {
			foundRm = true
		}
		if strings.Contains(cmd, "cat secret") {
			foundCat = true
		}
	}

	if !foundRm {
		t.Errorf("subshell $() command was not extracted: %v", commands)
	}
	if !foundCat {
		t.Errorf("backtick subshell command was not extracted: %v", commands)
	}
}

func TestSecurityValidationBlocksDestructiveCommands(t *testing.T) {
	sup := NewSupervisor()
	sup.SetBlockedCommands([]string{"rm -rf /", "mkfs"})

	err := sup.ValidateCommand("echo hello; rm -rf /")
	if err == nil {
		t.Fatal("expected error on blocked command, got nil")
	}

	err = sup.ValidateCommand("echo hello\nmkfs /dev/sda")
	if err == nil {
		t.Fatal("expected error on blocked command after newline, got nil")
	}

	err = sup.ValidateCommand("echo safe command")
	if err != nil {
		t.Fatalf("unexpected error on safe command: %v", err)
	}
}

func TestProcessLifecycleAndPagination(t *testing.T) {
	sup := NewSupervisor()

	cmd := "echo line1; echo line2; echo line3; echo line4; echo line5"
	res, err := sup.StartProcess(cmd, 5000, "")
	if err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	if res.PID <= 0 {
		t.Fatalf("invalid PID: %d", res.PID)
	}

	// Wait briefly for execution to finish
	time.Sleep(200 * time.Millisecond)

	// Read offset 0 (watermark)
	read1, err := sup.ReadProcessOutput(res.PID, 1, 2)
	if err != nil {
		t.Fatalf("failed to read output: %v", err)
	}

	if !strings.Contains(read1.Output, "line2") {
		t.Errorf("expected output to contain line2, got: %s", read1.Output)
	}

	// Read tail (negative offset: last 2 lines)
	readTail, err := sup.ReadProcessOutput(res.PID, -2, 2)
	if err != nil {
		t.Fatalf("failed to read tail output: %v", err)
	}

	if !strings.Contains(readTail.Output, "line4") || !strings.Contains(readTail.Output, "line5") {
		t.Errorf("expected tail output to contain line4 and line5, got: %s", readTail.Output)
	}

	// Check sessions listing
	sessions := sup.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].PID != res.PID {
		t.Errorf("session PID mismatch: expected %d, got %d", res.PID, sessions[0].PID)
	}
}

func TestProcessTermination(t *testing.T) {
	sup := NewSupervisor()

	res, err := sup.StartProcess("sleep 30", 500, "")
	if err != nil {
		t.Fatalf("failed to start sleep process: %v", err)
	}

	err = sup.TerminateProcess(res.PID, true)
	if err != nil {
		t.Fatalf("failed to terminate process: %v", err)
	}

	sessions := sup.ListSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].State != StateTerminated {
		t.Errorf("expected state to be terminated, got: %s", sessions[0].State)
	}
}
