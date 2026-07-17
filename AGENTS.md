# AGENTS.md - texpand

Comprehensive project docs are in `CONTRIBUTING.md` and `README.md`. This file
contains the mandatory guardrails for AI agents and automation working in this
repository.

## Git Workflow

- Never commit directly to `main` unless the user explicitly asks for it.
- Work on branches with one of these prefixes: `feature/`, `fix/`,
  `refactor/`, `docs/`, `chore/`.
- Do not force-push `main`.
- Do not delete protected or shared branches, or another contributor's active
  branch, without explicit owner consent.
- Do not rewrite published history without explicit consent.
- Follow [CONTRIBUTING.md](CONTRIBUTING.md) for the recommended workflow,
  including commit and merge strategy.
- Before opening a PR, run the tests relevant to the changed parts.
- If tests are not available yet, state that clearly in the PR description.

## Documentation Rules

- Update `README.md` for end-user behavior, setup, and troubleshooting.
- Update `CONTRIBUTING.md` for development workflow changes.
- Update `AGENTS.md` for AI agent workflow changes.
- If documentation does not need changes, say that explicitly in the PR
  description.

## Architecture

```
main.go           → Entry point, CLI commands (init, version, migrate), signal handling, device hotplug loop
keyboard.go       → Enumerates /dev/input/ devices, manages keyboard monitors, reports monitor exits
keymap.go         → US/International evdev keycode → character mapping (normal + shifted)
expander.go       → Rolling keystroke buffer, trigger matching, clipboard paste, virtual keyboard
config.go         → Loads app config (config.yml) and YAML match files from ~/.config/texpand/match/
config_defaults.go→ Embedded default configs (//go:embed), extracted on `texpand init`
migrate.go        → Versioned config migration system (texpand migrate)
variables.go      → Variable resolution (date type with offset), {{ref}} expansion
strftime.go       → Strftime token replacement (%Y, %m, %d, etc.)
autocorrect.go    → Polish autocorrection engine
autocorrect_config.go → Autocorrection configuration loading
```

### Control flow

```
main() → ensureWaylandEnv() → LoadAppConfig() → LoadConfig()
       → FindKeyboards() → fsnotify.NewWatcher() (watch config + match dirs + /dev/input)
       → MonitorKeyboard() goroutines (one per keyboard) → event channel
       → select loop:
           keyboard event  → Expander.HandleEvent() → buffer → trigger match
           keyboard exit   → remove monitor → debounce keyboard rescan
           /dev/input event or rescan ticker → RefreshKeyboardMonitors()
           config fsnotify event → debounce timer (500ms)
           config debounce fires → LoadAppConfig() + LoadConfig() → Expander.Reload()
       → resolveReplacement() → clipboardPaste() + Ctrl+V
```

### Key patterns

- **Single package (`main`)** — all files are in package main, no internal packages
- **Goroutine per keyboard** — each keyboard device gets its own monitoring goroutine, events are funneled into a single channel
- **Keyboard hotplug recovery** — monitors `/dev/input` and rescans periodically so recreated event nodes are picked up after USB hub, KVM, or monitor input resets
- **Rolling buffer with suffix matching** — buffer is capped to the longest trigger length, matches check `strings.HasSuffix`
- **Longest-trigger-first sorting** — prevents partial false matches
- **Hot-reload** — watches config directory via fsnotify, debounces file changes (500ms), reloads config through the main event loop without restarting
- **Clipboard preservation** — saves clipboard before paste, restores after
- **Timing delays** — strategic `time.Sleep` calls (8-50ms) between virtual keyboard operations for app responsiveness
- **Shift state tracking** — tracks shift key press/release to map correct character (normal vs shifted)
- **Configurable trigger mode** — `config.yml` sets the global trigger mode (`space` or `immediate`), applies to all matches

## Build and run

```bash
go build              # compile
go install ./...      # install to $GOPATH/bin
./texpand             # run (needs input group membership + udev rules)
./texpand init        # extract default config to ~/.config/texpand/
./texpand version     # print version
./texpand migrate     # migrate config files to latest format
```

No Makefile. No test suite currently. Version is `"dev"` locally; GoReleaser injects the real version via ldflags. `go install` reads the version from `runtime/debug.ReadBuildInfo`.

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/bendahl/uinput` | Virtual keyboard (backspace, Ctrl+V, arrow keys) |
| `github.com/holoplot/go-evdev` | Raw keyboard event reading from /dev/input/ |
| `gopkg.in/yaml.v3` | YAML config parsing |
| `github.com/fsnotify/fsnotify` | File system watching for config hot-reload |

Runtime: `wl-copy` and `wl-paste` from `wl-clipboard`.

## Config format

### Global settings (`~/.config/texpand/config.yml`)

```yaml
config_version: 1

# "space" (default) - triggers fire when space is pressed after the trigger
# "immediate" - triggers fire as soon as the trigger is typed
trigger_mode: space
```

- `config_version` — tracks which migrations have been applied (current: 1). Do not edit manually.

### Match files (`~/.config/texpand/match/*.yml`)

Espanso-compatible subset.

```yaml
global_vars:
  - name: _date
    type: date
    params:
      format: "%d/%m/%Y"
      offset: 0          # optional: seconds offset for date math

matches:
  - trigger: "]a"
    replace: "á"

  - triggers: ["'binsh", "'#!"]
    replace: "#!/bin/sh"

  - trigger: "'date"
    replace: "{{_date}}"  # variable reference
```

- `trigger_mode` in `config.yml` controls all matches globally (`space` or `immediate`)
- `$|$` in replacement — cursor positioning marker (moves cursor back after paste)
- `{{varname}}` — resolved from global_vars or match-level vars

## Conventions

- **Commits**: follow [Conventional Commits](https://www.conventionalcommits.org/) (e.g. `feat:`, `fix:`, `docs:`, `refactor:`, `chore:`)
- **Error handling**: `if err != nil` with `fmt.Errorf("context: %w", err)` wrapping
- **Naming**: PascalCase for exported types/functions, camelCase for locals, UPPER_SNAKE for evdev constants
- **Output**: `fmt.Printf` for normal output, `fmt.Fprintf(os.Stderr, ...)` for errors/warnings
- **Debug logging** — `--debug` / `-d` flag enables verbose logging to stderr via `dbg()` helper
- **No tests** — project has no test files currently
- **Config migrations** — versioned migration system in `migrate.go`. `config_version` in `config.yml` tracks applied migrations. To add a new migration: append to the `migrations` slice with the next version number, increment `latestConfigVersion`

## Platform requirements

- Linux with Wayland compositor
- User must be in `input` group
- `/dev/uinput` must be writable (udev rule provided in `99-uinput.rules`)
- US/International keyboard layout assumed for symbol key mapping
