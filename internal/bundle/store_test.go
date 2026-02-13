package bundle

import "testing"

func TestCreateListGetDelete(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := Create("daily", []Entry{
		{HostAlias: "db", ForwardSelector: "0"},
		{HostAlias: "api", ForwardSelector: "127.0.0.1:18080:localhost:8080"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	all, err := LoadAll()
	if err != nil {
		t.Fatalf("load all: %v", err)
	}
	if len(all) != 1 || all[0].Name != "daily" {
		t.Fatalf("unexpected bundles: %+v", all)
	}

	got, err := Get("daily")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("expected two entries, got %d", len(got.Entries))
	}

	if err := Delete("daily"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	all, err = LoadAll()
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("expected no bundles, got %d", len(all))
	}
}

func TestCreateValidatesInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := Create("", []Entry{{HostAlias: "db"}}); err == nil {
		t.Fatal("expected error for empty name")
	}
	if err := Create("x", nil); err == nil {
		t.Fatal("expected error for empty entries")
	}
	if err := Create("x", []Entry{{HostAlias: ""}}); err == nil {
		t.Fatal("expected error for empty host alias")
	}
}
