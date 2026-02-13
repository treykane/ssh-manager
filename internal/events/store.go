package events

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/treykane/ssh-manager/internal/appconfig"
	"github.com/treykane/ssh-manager/internal/model"
)

// Event is one tunnel lifecycle record persisted to events.jsonl.
type Event struct {
	Timestamp time.Time         `json:"timestamp"`
	TunnelID  string            `json:"tunnel_id,omitempty"`
	HostAlias string            `json:"host_alias,omitempty"`
	EventType string            `json:"event_type"`
	State     model.TunnelState `json:"state,omitempty"`
	Message   string            `json:"message,omitempty"`
	PID       int               `json:"pid,omitempty"`
}

// Query controls event filtering and bounded reads.
type Query struct {
	HostAlias string
	TunnelID  string
	EventType string
	Since     time.Time
	Limit     int
}

// Store provides append/read access to the local event journal.
type Store struct{}

func NewStore() *Store {
	return &Store{}
}

func filePath() (string, error) {
	dir, err := appconfig.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "events.jsonl"), nil
}

// Append writes a single event as one JSON line.
func (s *Store) Append(evt Event) error {
	path, err := filePath()
	if err != nil {
		return err
	}
	if evt.Timestamp.IsZero() {
		evt.Timestamp = time.Now().UTC()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	b, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

// Read returns events in append order, filtered by query, with optional limit.
func (s *Store) Read(q Query) ([]Event, error) {
	path, err := filePath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var evt Event
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		if !matches(evt, q) {
			continue
		}
		out = append(out, evt)
		if q.Limit > 0 && len(out) > q.Limit {
			out = out[len(out)-q.Limit:]
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan events: %w", err)
	}
	return out, nil
}

func matches(evt Event, q Query) bool {
	if strings.TrimSpace(q.HostAlias) != "" && evt.HostAlias != q.HostAlias {
		return false
	}
	if strings.TrimSpace(q.TunnelID) != "" && evt.TunnelID != q.TunnelID {
		return false
	}
	if strings.TrimSpace(q.EventType) != "" && evt.EventType != q.EventType {
		return false
	}
	if !q.Since.IsZero() && evt.Timestamp.Before(q.Since) {
		return false
	}
	return true
}
