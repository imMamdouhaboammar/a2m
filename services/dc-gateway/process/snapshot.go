package process

import (
	"fmt"
	"time"
)

type SnapshotLine struct {
	Timestamp time.Time `json:"timestamp"`
	Stream    string    `json:"stream"`
	Text      string    `json:"text"`
}

// SnapshotOutput returns a bounded copy of recent output for a process without
// changing ReadWatermark used by agent-facing ReadProcessOutput.
func (s *Supervisor) SnapshotOutput(pid, maxLines int) ([]SnapshotLine, error) {
	s.mu.RLock()
	session, exists := s.sessions[pid]
	s.mu.RUnlock()
	if !exists {
		return nil, fmt.Errorf("session %d not found", pid)
	}
	if maxLines < 1 || maxLines > 500 {
		maxLines = 250
	}
	session.Mu.RLock()
	defer session.Mu.RUnlock()
	start := len(session.Lines) - maxLines
	if start < 0 {
		start = 0
	}
	lines := make([]SnapshotLine, 0, len(session.Lines[start:]))
	for _, item := range session.Lines[start:] {
		lines = append(lines, SnapshotLine{Timestamp: item.Timestamp, Stream: item.Stream, Text: item.Text})
	}
	return lines, nil
}
