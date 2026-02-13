package events

import (
	"testing"
	"time"
)

func TestStoreAppendReadAndFilters(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s := NewStore()

	base := time.Now().Add(-2 * time.Hour).UTC()
	seed := []Event{
		{Timestamp: base, TunnelID: "a", HostAlias: "api", EventType: "start_requested"},
		{Timestamp: base.Add(10 * time.Minute), TunnelID: "a", HostAlias: "api", EventType: "start_succeeded"},
		{Timestamp: base.Add(20 * time.Minute), TunnelID: "b", HostAlias: "db", EventType: "start_failed"},
	}
	for _, evt := range seed {
		if err := s.Append(evt); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	all, err := s.Read(Query{})
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 events, got %d", len(all))
	}

	hostOnly, err := s.Read(Query{HostAlias: "api"})
	if err != nil {
		t.Fatalf("read host: %v", err)
	}
	if len(hostOnly) != 2 {
		t.Fatalf("expected 2 api events, got %d", len(hostOnly))
	}

	limited, err := s.Read(Query{Limit: 1})
	if err != nil {
		t.Fatalf("read limit: %v", err)
	}
	if len(limited) != 1 || limited[0].TunnelID != "b" {
		t.Fatalf("unexpected limited result: %+v", limited)
	}

	since, err := s.Read(Query{Since: base.Add(15 * time.Minute)})
	if err != nil {
		t.Fatalf("read since: %v", err)
	}
	if len(since) != 1 || since[0].TunnelID != "b" {
		t.Fatalf("unexpected since result: %+v", since)
	}
}
