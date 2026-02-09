package ui

import (
	"testing"
)

func TestParseQuickConnect(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHost string
		wantUser string
		wantPort int
		wantErr  bool
	}{
		{
			name:     "hostname only",
			input:    "example.com",
			wantHost: "example.com",
			wantPort: 22,
		},
		{
			name:     "user@hostname",
			input:    "deploy@example.com",
			wantHost: "example.com",
			wantUser: "deploy",
			wantPort: 22,
		},
		{
			name:     "hostname:port",
			input:    "example.com:2222",
			wantHost: "example.com",
			wantPort: 2222,
		},
		{
			name:     "user@hostname:port",
			input:    "deploy@example.com:2222",
			wantHost: "example.com",
			wantUser: "deploy",
			wantPort: 2222,
		},
		{
			name:     "IP address",
			input:    "192.168.1.1",
			wantHost: "192.168.1.1",
			wantPort: 22,
		},
		{
			name:     "user@IP:port",
			input:    "root@10.0.0.1:22",
			wantHost: "10.0.0.1",
			wantUser: "root",
			wantPort: 22,
		},
		{
			name:    "empty input",
			input:   "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
		{
			name:     "with leading/trailing spaces",
			input:    "  example.com  ",
			wantHost: "example.com",
			wantPort: 22,
		},
		{
			name:     "invalid port falls back to hostname with colon",
			input:    "example.com:notaport",
			wantHost: "example.com:notaport",
			wantPort: 22,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, err := parseQuickConnect(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.HostName != tt.wantHost {
				t.Errorf("hostname: want %q, got %q", tt.wantHost, h.HostName)
			}
			if h.User != tt.wantUser {
				t.Errorf("user: want %q, got %q", tt.wantUser, h.User)
			}
			if h.Port != tt.wantPort {
				t.Errorf("port: want %d, got %d", tt.wantPort, h.Port)
			}
			if !h.IsAdHoc {
				t.Error("expected IsAdHoc to be true")
			}
		})
	}
}
