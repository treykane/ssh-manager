# ssh-manager

A modern terminal application for managing SSH connections and tunnels, built with Go.

Browse your OpenSSH hosts, open sessions, and control port-forwarding tunnels —
all from an interactive dashboard or straight from the command line.

---

## Table of Contents

- [Key Features](#key-features)
- [Requirements](#requirements)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [TUI Dashboard](#tui-dashboard)
- [CLI Reference](#cli-reference)
  - [List Hosts](#list-hosts)
  - [Start Tunnels](#start-tunnels)
  - [Stop Tunnels](#stop-tunnels)
  - [Tunnel Status](#tunnel-status)
- [Configuration](#configuration)
  - [App Config](#app-config)
  - [Default Settings](#default-settings)
- [Development](#development)
- [Project Layout](#project-layout)
- [Notes](#notes)

---

## Key Features

| Category            | Details                                                                 |
| ------------------- | ----------------------------------------------------------------------- |
| **SSH Config**      | Parses `~/.ssh/config` with full `Include` support                      |
| **Host Normalization** | Extracts `HostName`, `User`, `Port`, `ProxyJump`, `IdentityFile`    |
| **Port Forwarding** | Reads `LocalForward` entries per host and manages their lifecycle       |
| **Tunnel Execution**| Spawns tunnels via the system `ssh` binary (no shell interpolation)     |
| **State Tracking**  | Tracks tunnel state: `starting` · `up` · `stopping` · `down` · `error` |
| **Persistence**     | Saves tunnel runtime state to your XDG config directory                 |
| **Output Formats**  | Human-readable table and JSON for scripting                             |
| **Graceful Errors** | Surfaces config parser warnings without hard failures                   |

---

## Requirements

- **Go** 1.25 or later
- **OpenSSH client** (`ssh`) available on your `PATH`

---

## Installation

### Build from source

```bash
go build ./cmd/ssh-manager
```

This produces a `./ssh-manager` binary in the current directory.

---

## Quick Start

```bash
# Launch the interactive TUI dashboard
./ssh-manager

# Or run directly without building
go run ./cmd/ssh-manager
```

---

## TUI Dashboard

The default command opens a Bubble Tea–powered dashboard for browsing hosts,
connecting to them, and toggling tunnels.

### Keybindings

| Key              | Action                                          |
| ---------------- | ----------------------------------------------- |
| `j` / `k` / `↑` / `↓` | Move selection                           |
| `Enter`          | Open an interactive SSH session to the selected host |
| `t`              | Toggle the first `LocalForward` tunnel for the selected host |
| `/`              | Enter filter mode                               |
| `r`              | Reload SSH config and tunnel snapshot            |
| `?`              | Toggle the help panel                           |
| `q` / `Ctrl+C`  | Quit (stops all managed tunnels)                |

---

## CLI Reference

### List Hosts

Print all parsed SSH hosts:

```bash
./ssh-manager list
```

### Start Tunnels

Start **all** `LocalForward` tunnels defined for a host:

```bash
./ssh-manager tunnel up <host>
```

Start a **single** forward by its 0-based index:

```bash
./ssh-manager tunnel up <host> --forward 0
```

Start a **specific** forward by address:

```bash
./ssh-manager tunnel up <host> --forward 127.0.0.1:15432:localhost:5432

# Shorthand (bind address defaults to 127.0.0.1):
./ssh-manager tunnel up <host> --forward 15432:localhost:5432
```

### Stop Tunnels

Stop a tunnel by its full ID:

```bash
./ssh-manager tunnel down '<host>|127.0.0.1:15432|localhost:5432'
```

Stop **all** active tunnels for a host:

```bash
./ssh-manager tunnel down <host>
```

### Tunnel Status

```bash
# Human-readable output
./ssh-manager tunnel status

# Machine-readable JSON
./ssh-manager tunnel status --json
```

<<<<<<< HEAD
#### JSON Schema
=======
Restart tunnel(s):

```bash
./ssh-manager tunnel restart '<host>|127.0.0.1:15432|localhost:5432'
./ssh-manager tunnel restart <host>
```

Security controls for tunnel startup:

```bash
./ssh-manager tunnel up <host> --host-key-policy accept-new
./ssh-manager tunnel up <host> --allow-public-bind
```

Run security audit:

```bash
./ssh-manager security audit
./ssh-manager security audit --json
```

## `tunnel status --json` schema
>>>>>>> 220769d (Security focused enhancements)

The `--json` output contains these stable fields per tunnel:

| Field            | Description                        |
| ---------------- | ---------------------------------- |
| `id`             | Unique tunnel identifier           |
| `host_alias`     | SSH host alias                     |
| `local`          | Local bind address and port        |
| `remote`         | Remote target address and port     |
| `state`          | Current tunnel state               |
| `pid`            | OS process ID of the `ssh` process |
| `uptime_seconds` | Seconds since the tunnel started   |
| `latency_ms`     | Last measured latency              |
| `last_error`     | Most recent error message, if any  |

<<<<<<< HEAD
---
=======
In dashboard mode:
- `j` / `k` or arrow keys: move selection
- `Enter`: open interactive SSH session to selected host
- `t`: toggle first `LocalForward` tunnel for selected host
- `T`: process all `LocalForward` entries for selected host
- `R`: restart first `LocalForward` tunnel for selected host
- `/`: filter mode
- `r`: reload SSH config and tunnel snapshot
- `?`: toggle help block
- `q` / `Ctrl+C`: quit (stops managed tunnels)
>>>>>>> 220769d (Security focused enhancements)

## Configuration

### App Config

Configuration and runtime files are stored in:

```text
$XDG_CONFIG_HOME/ssh-manager/     # if XDG_CONFIG_HOME is set
~/.config/ssh-manager/            # otherwise
```

| File             | Purpose                                  |
| ---------------- | ---------------------------------------- |
| `config.yaml`   | App settings (auto-created with defaults) |
| `runtime.json`  | Tunnel runtime state persistence          |

### Default Settings

```yaml
default_health_command: uptime
ui:
  refresh_seconds: 3
security:
  bind_policy: loopback-only
  host_key_policy: strict
  audit_log_enabled: false
  redact_errors: true
```

---

## Development

```bash
# Format
gofmt -w $(rg --files -g '*.go')

# Test
go test ./...

# Build
go build ./cmd/ssh-manager

# Shortcuts (if using the Makefile)
make build
make test
make run
make lint
```

---

## Project Layout

```text
cmd/ssh-manager/main.go          Entrypoint
internal/
  cli/root.go                    Cobra command definitions
  ui/ui.go                       Bubble Tea TUI dashboard
  config/parser.go               SSH config parsing (with Include support)
  sshclient/client.go            System ssh invocation
  tunnel/manager.go              Tunnel lifecycle supervision
  appconfig/config.go            App config & runtime path resolution
  model/types.go                 Shared type contracts
```

---

## Notes

- **Graceful degradation** — Parser warnings are surfaced without aborting.
- **Lifecycle-bound tunnels** — Quitting the TUI stops all managed tunnels.
- **No background daemon** — This project intentionally runs in the foreground in v1.
