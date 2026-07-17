# CLAUDE.md - texpand

This is the concise working guide for Claude/Codex in this repository. Mandatory
agent rules live in `AGENTS.md`; treat that file as authoritative.

## Project Snapshot

- **What:** Lightweight, single-binary Wayland text expander written in Go.
  Reads raw keyboard events via evdev, maintains a rolling keystroke buffer,
  and when a trigger matches it backspaces the trigger text, copies the
  replacement to clipboard, and pastes via Ctrl+V. Clipboard contents are
  preserved and restored after paste.
- **Status:** Core functionality is implemented: keyboard monitoring, trigger
  matching, clipboard paste, config hot-reload, variable resolution, config
  migrations. Polish autocorrection support has been added as an extended
  feature. No test suite yet.
- **Stack:** Go (single package `main`), `go-evdev`, `uinput`, `fsnotify`,
  `yaml.v3`, `wl-clipboard`.
- **Runtime constraints:** Linux with Wayland compositor, user in `input` group,
  writable `/dev/uinput`.

## Read First

- `AGENTS.md` - mandatory workflow and guardrails.
- `CONTRIBUTING.md` - branch, commit, and pull request strategy.
- `README.md` - setup, usage, and troubleshooting.

## Common Commands

```bash
go build              # compile
go vet ./...          # static analysis
gofmt -l .            # check formatting
go test ./...         # run tests
go test -race ./...   # tests with race detector
```

## Agent Working Notes

- All files are in `package main` - no internal packages.
- No logging framework; use `fmt.Printf` for output, `fmt.Fprintf(os.Stderr, ...)`
  for errors, and the `dbg()` helper for debug logging (`--debug` / `-d` flag).
- Error wrapping uses `fmt.Errorf("context: %w", err)`.
- The rolling buffer is capped to the longest trigger length; matching uses
  `strings.HasSuffix` with longest-trigger-first sorting.
- Config is loaded from `~/.config/texpand/` and hot-reloaded via fsnotify.
- Clipboard contents are saved before paste and restored after.
- No test suite currently.
- Version is `"dev"` locally; GoReleaser injects the real version via ldflags.
