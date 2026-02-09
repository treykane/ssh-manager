// Package sshclient tests verify the SSH argument composition logic used to
// construct command-line arguments for the system SSH binary.
//
// These tests focus on the BuildTunnelArgs method, which is responsible for
// assembling the correct -N and -L flags that tell SSH to set up local port
// forwarding without executing a remote command. Getting these arguments right
// is critical because:
//
//   - Incorrect -L syntax will cause SSH to fail with a cryptic error.
//   - Missing -N would cause SSH to open an interactive shell instead of a
//     tunnel-only connection.
//   - Address normalization (empty addresses → defaults) must produce valid
//     SSH arguments regardless of how the ForwardSpec was constructed.
//
// These tests do NOT start actual SSH processes or require network connectivity.
// They only verify that the argument slices are constructed correctly, making
// them fast, deterministic, and safe to run in any environment.
//
// Test naming convention:
//   - TestBuildTunnelArgs: verifies the core argument composition with a fully
//     specified ForwardSpec (explicit local and remote addresses).
package sshclient

import (
	"reflect"
	"testing"

	"github.com/treykane/ssh-manager/internal/model"
)

// TestBuildTunnelArgs verifies that BuildTunnelArgs produces the correct SSH
// command-line arguments for a tunnel with fully specified local and remote
// endpoints.
//
// The expected argument structure is:
//
//	["-N", "-L", "<localAddr>:<localPort>:<remoteAddr>:<remotePort>", "<hostAlias>"]
//
// Where:
//   - "-N" tells SSH not to execute a remote command (tunnel-only mode).
//   - "-L" specifies a local port forwarding rule.
//   - The forwarding spec is a single colon-separated string combining the local
//     bind address/port and the remote destination address/port.
//   - The host alias is the SSH config alias (e.g., "prod"), which SSH resolves
//     using the user's ~/.ssh/config to determine the actual hostname, user, port,
//     identity file, proxy jump chain, etc.
//
// This test uses a fully specified ForwardSpec (both LocalAddr and RemoteAddr are
// set explicitly) so it verifies the straightforward path without address
// normalization. Address normalization (empty → default) is handled by
// util.NormalizeAddr, which is tested implicitly through integration with the
// tunnel manager.
func TestBuildTunnelArgs(t *testing.T) {
	c := New()

	// Build arguments for a tunnel that forwards 127.0.0.1:8080 (local) to
	// localhost:80 (remote) through the "prod" SSH host.
	args := c.BuildTunnelArgs("prod", model.ForwardSpec{
		LocalAddr:  "127.0.0.1",
		LocalPort:  8080,
		RemoteAddr: "localhost",
		RemotePort: 80,
	})

	// Expected: ssh -N -L 127.0.0.1:8080:localhost:80 prod
	want := []string{"-N", "-L", "127.0.0.1:8080:localhost:80", "prod"}

	// Use reflect.DeepEqual for slice comparison — it checks both length
	// and element-by-element equality, catching subtle issues like extra
	// whitespace or reordered arguments.
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args mismatch\nwant=%v\n got=%v", want, args)
	}
}

// TestConnectAdHocCommand verifies that ConnectAdHocCommand produces the correct
// SSH command-line arguments for ad-hoc connections with explicit parameters.
func TestConnectAdHocCommand(t *testing.T) {
	c := New()

	tests := []struct {
		name string
		host model.HostEntry
		want []string
	}{
		{
			name: "hostname only",
			host: model.HostEntry{HostName: "example.com", Port: 22, IsAdHoc: true},
			want: []string{"ssh", "example.com"},
		},
		{
			name: "user and hostname",
			host: model.HostEntry{HostName: "example.com", User: "deploy", Port: 22, IsAdHoc: true},
			want: []string{"ssh", "deploy@example.com"},
		},
		{
			name: "custom port",
			host: model.HostEntry{HostName: "example.com", User: "deploy", Port: 2222, IsAdHoc: true},
			want: []string{"ssh", "-p", "2222", "deploy@example.com"},
		},
		{
			name: "with identity file",
			host: model.HostEntry{
				HostName: "example.com", User: "admin", Port: 22,
				IdentityFile: "~/.ssh/id_ed25519", IsAdHoc: true,
			},
			want: []string{"ssh", "-i", "~/.ssh/id_ed25519", "admin@example.com"},
		},
		{
			name: "with proxy jump",
			host: model.HostEntry{
				HostName: "internal.server", User: "admin", Port: 22,
				ProxyJump: "bastion", IsAdHoc: true,
			},
			want: []string{"ssh", "-J", "bastion", "admin@internal.server"},
		},
		{
			name: "all options",
			host: model.HostEntry{
				HostName: "internal.server", User: "admin", Port: 2222,
				IdentityFile: "~/.ssh/key", ProxyJump: "bastion", IsAdHoc: true,
			},
			want: []string{"ssh", "-p", "2222", "-i", "~/.ssh/key", "-J", "bastion", "admin@internal.server"},
		},
		{
			name: "no user",
			host: model.HostEntry{HostName: "server.local", Port: 2222, IsAdHoc: true},
			want: []string{"ssh", "-p", "2222", "server.local"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := c.ConnectAdHocCommand(tt.host)
			got := cmd.Args
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args mismatch\nwant=%v\n got=%v", tt.want, got)
			}
		})
	}
}

// TestConnectCommandDispatch verifies that ConnectCommand dispatches to
// ConnectAdHocCommand when IsAdHoc is true and uses alias-based connection
// when IsAdHoc is false.
func TestConnectCommandDispatch(t *testing.T) {
	c := New()

	// Config-based host: should use alias only.
	configHost := model.HostEntry{Alias: "prod-db", HostName: "db.example.com", Port: 22}
	cmd := c.ConnectCommand(configHost)
	wantConfig := []string{"ssh", "prod-db"}
	if !reflect.DeepEqual(cmd.Args, wantConfig) {
		t.Fatalf("config host args mismatch\nwant=%v\n got=%v", wantConfig, cmd.Args)
	}

	// Ad-hoc host: should use explicit args.
	adHocHost := model.HostEntry{HostName: "db.example.com", User: "root", Port: 22, IsAdHoc: true}
	cmd = c.ConnectCommand(adHocHost)
	wantAdHoc := []string{"ssh", "root@db.example.com"}
	if !reflect.DeepEqual(cmd.Args, wantAdHoc) {
		t.Fatalf("ad-hoc host args mismatch\nwant=%v\n got=%v", wantAdHoc, cmd.Args)
	}
}
