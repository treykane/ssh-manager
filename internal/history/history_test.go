package history

import (
	"testing"
	"time"

	"github.com/treykane/ssh-manager/internal/model"
)

func TestTouchAndLastUsed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Touch("api"); err != nil {
		t.Fatalf("touch: %v", err)
	}
	got, err := LastUsed()
	if err != nil {
		t.Fatalf("last used: %v", err)
	}
	if got["api"] <= 0 {
		t.Fatalf("expected timestamp for api, got %+v", got)
	}
}

func TestSortHostsRecent(t *testing.T) {
	hosts := []model.HostEntry{
		{Alias: "db"},
		{Alias: "api"},
		{Alias: "cache"},
	}
	now := time.Now().Unix()
	sorted := SortHostsRecent(hosts, map[string]int64{
		"api": now,
		"db":  now - 60,
	})
	if sorted[0].Alias != "api" {
		t.Fatalf("expected api first, got %s", sorted[0].Alias)
	}
}
