package ui

import (
	"testing"
	"time"

	"github.com/treykane/ssh-manager/internal/history"
	"github.com/treykane/ssh-manager/internal/model"
)

func TestApplyFilter_RecentFirstSort(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := history.Touch("db"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	if err := history.Touch("api"); err != nil {
		t.Fatal(err)
	}

	m := dashboardModel{
		hosts: []model.HostEntry{
			{Alias: "db"},
			{Alias: "api"},
			{Alias: "cache"},
		},
		recentFirst: true,
	}
	m.applyFilter()
	if len(m.filtered) < 2 {
		t.Fatalf("unexpected filtered hosts: %+v", m.filtered)
	}
	if m.filtered[0].Alias != "api" {
		t.Fatalf("expected most recent host first, got %s", m.filtered[0].Alias)
	}
}
