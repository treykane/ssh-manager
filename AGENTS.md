# AGENTS.md

## Purpose
This file gives coding agents project-specific guidance for working in this repository.

## Project Overview
- Name: `ssh-manager`
- Language: Go
- App type: terminal app with:
  - TUI dashboard (Bubble Tea)
  - CLI commands (Cobra)
- Core behavior:
  - Parse `~/.ssh/config` (with `Include`)
  - Connect to hosts using system `ssh`
  - Start/stop/inspect SSH tunnels from `LocalForward`

## Current Code Layout
- Entry point:
  - `cmd/ssh-manager/main.go`
- CLI:
  - `internal/cli/root.go`
- TUI:
  - `internal/ui/ui.go`
- SSH config parsing:
  - `internal/config/parser.go`
- Domain models:
  - `internal/model/types.go`
- SSH process execution:
  - `internal/sshclient/client.go`
- Tunnel supervision/runtime:
  - `internal/tunnel/manager.go`
- App config + runtime file paths:
  - `internal/appconfig/config.go`
- Tests:
  - `internal/config/parser_test.go`
  - `internal/sshclient/client_test.go`
  - `internal/tunnel/manager_test.go`
  - `tests/fixtures/ssh_config_basic`

## Local Commands
- Format:
  - `gofmt -w $(rg --files -g '*.go')`
- Test:
  - `go test ./...`
- Build:
  - `go build ./cmd/ssh-manager`
- Makefile shortcuts:
  - `make test`
  - `make build`
  - `make run`
  - `make lint`

## Non-Negotiable Rules
- Do not replace system `ssh` behavior with shell interpolation; keep argument-safe `exec.Command` usage.
- Keep tunnel lifecycle tied to app lifecycle unless explicitly changing product scope.
- Preserve JSON field stability for `tunnel status --json` (`id`, `host_alias`, `local`, `remote`, `state`, `pid`, `uptime_seconds`, `latency_ms`, `last_error`).
- Keep parser resilient: unsupported/malformed directives should degrade gracefully with warnings.
- Any test that touches config/runtime paths must isolate `XDG_CONFIG_HOME` via `t.TempDir()`.

## Coding Conventions
- Prefer small, focused functions over large handlers.
- Keep package boundaries clean:
  - Parsing logic stays in `internal/config`.
  - Process launching stays in `internal/sshclient`.
  - State supervision stays in `internal/tunnel`.
  - UI orchestration stays in `internal/ui`.
- Avoid circular dependencies across `internal/*`.
- Keep structs in `internal/model` as shared contracts.

## Testing Guidance
- Add/update tests for any behavior change.
- Minimum expectations per change:
  - Parser change -> `internal/config/parser_test.go`
  - Tunnel state change -> `internal/tunnel/manager_test.go`
  - SSH argument composition change -> `internal/sshclient/client_test.go`
- Keep tests deterministic; do not depend on user machine `~/.ssh/config`.

## Safe Change Workflow
1. Read impacted package(s) and tests first.
2. Implement the smallest coherent change.
3. Run `gofmt` on touched Go files.
4. Run `go test ./...`.
5. Run `go build ./cmd/ssh-manager`.
6. Summarize what changed, risks, and follow-ups.

## Known Constraints / Gaps
- Tunnel runtime persistence currently stores state in XDG config dir (`runtime.json`).
- No persistent daemon mode (by design for v1).
- Group health-check split view is not fully implemented yet.

## If You Add Features
- Keep CLI and TUI behaviors aligned (shared backend logic, not duplicated business rules).
- Prefer additive flags/subcommands to avoid breaking existing command usage.
- Document user-visible behavior changes in this file and, if added later, in README.
