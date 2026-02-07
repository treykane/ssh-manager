# ssh-manager

`ssh-manager` is a Go terminal app for working with SSH hosts and local forwards from your OpenSSH config.

It provides:
- A TUI dashboard (default command) for browsing hosts, connecting, and toggling tunnels
- CLI commands for listing hosts and managing tunnel lifecycle
- SSH config parsing with `Include` support and warning-based graceful degradation
- Tunnel runtime persistence in your XDG config directory

## Features

- Parses `~/.ssh/config` and included files
- Normalizes host entries (`HostName`, `User`, `Port`, `ProxyJump`, `IdentityFile`)
- Extracts `LocalForward` entries per host
- Starts tunnels via system `ssh` (no shell interpolation)
- Tracks tunnel state (`starting`, `up`, `stopping`, `down`, `error`)
- JSON output for tunnel status

## Requirements

- Go 1.24+
- OpenSSH client (`ssh`) available on `PATH`

## Build

```bash
go build ./cmd/ssh-manager
```

Binary path (default Go output):
- `./ssh-manager`

## Run

Start TUI dashboard:

```bash
go run ./cmd/ssh-manager
# or
./ssh-manager
```

## CLI Usage

List parsed hosts:

```bash
./ssh-manager list
```

Start tunnel(s) from a host's `LocalForward` entries:

```bash
./ssh-manager tunnel up <host>
```

Start one forward by index (0-based):

```bash
./ssh-manager tunnel up <host> --forward 0
```

Start one explicit forward:

```bash
./ssh-manager tunnel up <host> --forward 127.0.0.1:15432:localhost:5432
# shorthand also supported:
./ssh-manager tunnel up <host> --forward 15432:localhost:5432
```

Stop by tunnel ID:

```bash
./ssh-manager tunnel down '<host>|127.0.0.1:15432|localhost:5432'
```

Stop all active tunnels for a host alias:

```bash
./ssh-manager tunnel down <host>
```

Show tunnel status:

```bash
./ssh-manager tunnel status
./ssh-manager tunnel status --json
```

## `tunnel status --json` schema

Stable JSON fields:
- `id`
- `host_alias`
- `local`
- `remote`
- `state`
- `pid`
- `uptime_seconds`
- `latency_ms`
- `last_error`

## TUI Controls

In dashboard mode:
- `j` / `k` or arrow keys: move selection
- `Enter`: open interactive SSH session to selected host
- `t`: toggle first `LocalForward` tunnel for selected host
- `/`: filter mode
- `r`: reload SSH config and tunnel snapshot
- `?`: toggle help block
- `q` / `Ctrl+C`: quit (stops managed tunnels)

## Config and Runtime Paths

App config directory:
- `$XDG_CONFIG_HOME/ssh-manager` if `XDG_CONFIG_HOME` is set
- otherwise `~/.config/ssh-manager`

Files:
- `config.yaml` (auto-created with defaults)
- `runtime.json` (tunnel runtime persistence)

Default `config.yaml` values:

```yaml
default_health_command: uptime
ui:
  refresh_seconds: 3
```

## Development

Format:

```bash
gofmt -w $(rg --files -g '*.go')
```

Test:

```bash
go test ./...
# or
make test
```

Build:

```bash
go build ./cmd/ssh-manager
# or
make build
```

Run shortcuts:

```bash
make run
make lint
```

## Project Layout

- `cmd/ssh-manager/main.go` - entrypoint
- `internal/cli/root.go` - Cobra commands
- `internal/ui/ui.go` - Bubble Tea dashboard
- `internal/config/parser.go` - SSH config parsing with `Include`
- `internal/sshclient/client.go` - system `ssh` invocation
- `internal/tunnel/manager.go` - tunnel lifecycle supervision
- `internal/appconfig/config.go` - app config/runtime paths
- `internal/model/types.go` - shared contracts

## Notes

- Parser warnings are surfaced without failing hard where possible.
- Tunnel state is tied to app lifecycle; quitting the TUI stops managed tunnels.
- This project intentionally does not run as a persistent daemon in v1.
