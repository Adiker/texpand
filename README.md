# texpand

Lightweight Wayland text expander **with system-wide Polish diacritics autocorrection** (`zolw ` → `żółw `). Reads raw keyboard events via `evdev`, types replacements directly via `uinput`. Works on any Wayland compositor (KDE, GNOME, Hyprland, Sway, etc.); the autocorrection targets KDE Plasma Wayland with the Polish (Programmer) layout.

Single static binary. YAML config (espanso-compatible format). Zero runtime dependencies (optional: `hunspell-pl` for autocorrection, `wtype` for Unicode, `wl-clipboard` as last-resort fallback).

> **Warning**: This was vibe coded. It works, but don't expect anything from it xD.

## How it works

```
[Keyboard] ──evdev──→ texpand ──uinput──→ [Any App]
```

1. Monitors `/dev/input/event*` devices via evdev (non-exclusive)
2. Maintains a rolling buffer of recent keystrokes
3. On match: backspaces the trigger, types the replacement via uinput (falls back to `wtype` for Unicode, then clipboard paste as last resort)

texpand watches `/dev/input` while it runs. If a keyboard disappears and
reappears, for example when a monitor with an attached USB hub changes input or
powers off and on, texpand rescans devices and starts monitoring the new event
node without requiring a service restart.

Two trigger modes (set globally in `config.yml`):

- **Space** (default): fires when space is pressed after the trigger
- **Immediate**: fires as soon as the trigger is typed

Config changes are picked up automatically — no restart needed.

## Polish autocorrection

Typing Polish without reaching for AltGr: finish a word without diacritics
and texpand fixes it the moment you hit a word boundary. Ordinary typing
stays latency-free — the per-keystroke path is an in-memory buffer update
(~20 ns, zero allocations, no dictionary access); the dictionary is only
consulted when Space/punctuation commits a word (~0.0001 ms per lookup).

```
zolw␣    → żółw␣          Zolw.    → Żółw.
zrodlo,  → źródło,        WLASNIE! → WŁAŚNIE!
pisze␣   → pisze␣         (valid word, "piszę" also exists → left alone)
laska␣   → laska␣         (ambiguous with "łaska" → left alone)
```

**Safe mode** (the only mode in v1) corrects a word only when *all* of:
it contains no diacritics and only letters; it is **not** itself a valid
Polish word; the ASCII-folded dictionary lookup yields **exactly one**
candidate; and the case pattern is lower/Title/UPPER (mixed case is
skipped). Hyphenated compounds (`bialo-czerwony`), contractions,
identifiers, paths and e-mails are never touched.

- **Undo**: press Backspace right after a correction to get your original
  word back (`żółw ` + Backspace → `zolw`). Anything else commits it.
- **Toggle**: `ctrl+alt+slash` (configurable), or
  `texpand autocorrect enable|disable|toggle|status`.
- **Boundaries**: Space, `. , ! ? : ;` and `) ] } "` by default. Enter and
  Tab commit the word but do **not** correct by default — Enter often
  submits a chat message or form and Tab moves focus, so rewriting after
  the fact could edit the wrong widget. Opt in with
  `correct_on: {enter: true, tab: true}`.
- **App exclusions**: no corrections in terminals, IDEs/code editors, or
  remote-desktop apps by default (configurable, `*` globs supported). On
  KDE Plasma the active app is tracked via a tiny KWin script loaded at
  runtime; if detection is unavailable, `on_unknown_app` decides
  (default: correct).
- **Dictionary**: the system Hunspell Polish dictionary
  (`pacman -S hunspell-pl`, i.e. `/usr/share/hunspell/pl_PL.dic`). It is
  expanded to ~2.2 M inflected forms at first start (~3.5 s, in the
  background — typing is never blocked) and cached in
  `~/.cache/texpand/pl-index.cache` (validated against the dictionary
  path/size/mtime; delete it any time).
  Changing `dictionary` or `cache` hot-reloads the index. A failed load is
  reported by `texpand autocorrect status`; run `texpand autocorrect enable`
  after fixing the cause to retry without restarting the daemon.
- **Output**: Polish characters are typed directly through uinput AltGr
  combinations (Polish Programmer layout) — no subprocess and no
  clipboard involved. `wtype` is the fallback; clipboard paste exists but
  is off unless you set `allow_clipboard_fallback: true`.

See `docs/architecture.md` for the full design and the documented edge
cases; configuration reference is in the `autocorrect:` section of
[defaults/config.yml](defaults/config.yml).

### Performance

Reproduce with (requires `hunspell-pl` for the real-dictionary numbers):

```bash
go test -bench . -benchtime 2s ./internal/correct/ ./internal/dict/
```

Measured on a desktop Ryzen (go 1.26, real `pl_PL` dictionary, 2.2 M word
forms + 2.2 M candidate pairs):

```
BenchmarkOrdinaryKey              20.5 ns/op    0 B/op   0 allocs/op
BenchmarkWordCommit              239 ns/op     40 B/op   3 allocs/op
BenchmarkLookupUniqueCandidate    85.3 ns/op    0 B/op   0 allocs/op
BenchmarkLookupAmbiguous          88.9 ns/op    0 B/op   0 allocs/op
BenchmarkLookupMiss               83.1 ns/op    0 B/op   0 allocs/op
BenchmarkIsWord                   79.9 ns/op    0 B/op   0 allocs/op
BenchmarkFullBoundaryDecision    131 ns/op      0 B/op   0 allocs/op
BenchmarkBuildRealDictionary    3.41 s/op                (once, cached)
```

Ordinary keystrokes never touch the dictionary or the disk; the full
word-boundary decision against the real dictionary is ~0.13 µs — about
four orders of magnitude below the 1 ms perceptibility budget.

## Install

### Arch Linux / EndeavourOS (package)

```bash
sudo pacman -S --needed hunspell-pl go base-devel
git clone https://github.com/andresousadotpt/texpand && cd texpand/packaging
makepkg -si
texpand init
systemctl --user enable --now texpand.service
```

The package installs the binary, the systemd user unit, a udev rule for
`/dev/uinput` and a modules-load entry. You still need to be in the
`input` group (see below) and to re-login once.

To uninstall: `sudo pacman -R texpand`, then optionally remove
`~/.config/texpand`, `~/.cache/texpand`, and take yourself out of the
`input` group (`sudo gpasswd -d $USER input`).

### go install (manual)

```bash
go install github.com/andresousadotpt/texpand@latest
```

### Initialize config

```bash
texpand init
```

Creates `~/.config/texpand/match/` with default YAML trigger files.

### Set up permissions

texpand reads from `/dev/input/` and writes to `/dev/uinput`.

```bash
# Add your user to the input group
sudo usermod -aG input $USER

# Ensure the uinput module loads at boot
echo uinput | sudo tee /etc/modules-load.d/uinput.conf
sudo modprobe uinput

# Allow input group to write to /dev/uinput
sudo cp 99-uinput.rules /etc/udev/rules.d/99-uinput.rules
sudo udevadm control --reload-rules && sudo udevadm trigger

# Log out and back in for group change to take effect
```

### Systemd service

```bash
cp texpand.service ~/.config/systemd/user/texpand.service
systemctl --user daemon-reload
systemctl --user enable --now texpand.service
```

## Update

```bash
go install github.com/andresousadotpt/texpand@latest
systemctl --user restart texpand.service
```

After updating, run the migration command to update your config files to the latest format:

```bash
texpand migrate
```

This safely removes deprecated fields, creates `.bak` backups of modified files, and is idempotent (safe to run multiple times).

To pick up new default config files (without overwriting your existing ones):

```bash
texpand init
```

## Config format

YAML files in `~/.config/texpand/match/*.yml`. Espanso-compatible subset.

### Global settings (`config.yml`)

`~/.config/texpand/config.yml` controls global behavior:

```yaml
# "space" (default) - triggers fire on space
# "immediate" - triggers fire as soon as typed
trigger_mode: space
```

### Simple trigger

```yaml
matches:
    - trigger: "'date"
      replace: "{{_date}}"
```

### Multiple triggers for same replacement

```yaml
matches:
    - triggers: ["'binsh", "'#!"]
      replace: "#!/bin/sh"
```

### Date variables

```yaml
global_vars:
    - name: _date
      type: date
      params:
          format: "%d/%m/%Y"

matches:
    - trigger: "'date"
      replace: "{{_date}}"
```

### Date with offset (tomorrow/yesterday)

```yaml
matches:
    - trigger: "'tdate"
      replace: "{{tomorrow}}"
      vars:
          - name: tomorrow
            type: date
            params:
                format: "%a %m/%d/%Y"
                offset: 86400
```

### Cursor positioning

Use `$|$` to mark where the cursor should land after expansion:

```yaml
matches:
    - trigger: "'11"
      replace: "{{time_with_ampm}} - 1:1 with [$|$]"
```

### Supported strftime tokens

| Token | Meaning             | Example |
| ----- | ------------------- | ------- |
| `%Y`  | 4-digit year        | 2026    |
| `%m`  | Month (zero-padded) | 02      |
| `%d`  | Day (zero-padded)   | 23      |
| `%H`  | Hour 24h            | 14      |
| `%I`  | Hour 12h            | 02      |
| `%M`  | Minute              | 30      |
| `%S`  | Second              | 05      |
| `%p`  | AM/PM               | PM      |
| `%a`  | Short weekday       | Mon     |
| `%A`  | Full weekday        | Monday  |
| `%b`  | Short month         | Jan     |
| `%B`  | Full month          | January |

## All default triggers

### Accented characters (fire immediately)

| Trigger | Output | Trigger | Output |
| ------- | ------ | ------- | ------ |
| `]a`    | á      | `]A`    | Á      |
| `}a`    | à      | `}A`    | Á      |
| `~a`    | ã      | `~o`    | õ      |
| `]e`    | é      | `]E`    | É      |
| `}e`    | è      | `}E`    | È      |
| `]i`    | í      | `]I`    | Í      |
| `}i`    | ì      | `}I`    | Ì      |
| `]o`    | ó      | `]O`    | Ó      |
| `}o`    | ò      | `}O`    | Ò      |
| `]u`    | ú      | `]U`    | Ú      |
| `}u`    | ù      | `}U`    | Ù      |
| `'c,`   | ç      |         |        |

### Symbols (fire on space)

| Trigger | Output |
| ------- | ------ |
| `'deg`  | º      |
| `'...`  | ...    |
| `euros` | €      |

### Coding shortcuts (fire on space)

| Trigger          | Output                                    |
| ---------------- | ----------------------------------------- |
| `'binsh` / `'#!` | `#!/bin/sh`                               |
| `'gsm`           | `git switch main && git pull origin main` |
| `'gpomr`         | `git pull origin main --rebase`           |

### Date & time (fire on space)

### Usage

```
texpand [--debug|--debug-unsafe] [init|version|migrate|autocorrect <cmd>]
```

| Command               | Description                                                |
| --------------------- | ---------------------------------------------------------- |
| (none)                | Run texpand (monitor keyboards, expand, autocorrect)       |
| `init`                | Create default config in `~/.config/texpand/`              |
| `version`             | Print version                                              |
| `migrate`             | Migrate config files to the latest format                  |
| `autocorrect enable`  | Turn autocorrection on in the running daemon (via DBus)    |
| `autocorrect disable` | Turn autocorrection off                                    |
| `autocorrect toggle`  | Flip autocorrection                                        |
| `autocorrect status`  | Show enabled state, dictionary lifecycle/error and active app |

| Trigger  | Example output                              |
| -------- | ------------------------------------------- |
| `'n`     | `10:56 AM -`                                |
| `'date`  | `23/02/2026`                                |
| `'ddate` | `Mon 23/02/2026`                            |
| `'nn`    | `Mon 23/02/2026 - 10:56 AM -`               |
| `'st`    | `Mon 23/02/2026 - 10:56 AM - meeting start` |
| `'end`   | `Mon 23/02/2026 - 10:56 AM - meeting end`   |
| `'11`    | `10:56 AM - 1:1 with [cursor]`              |
| `'tdate` | Tomorrow's date                             |
| `'ydate` | Yesterday's date                            |

## Adding triggers

Edit or create YAML files in `~/.config/texpand/match/`. Changes are picked up automatically — no restart needed.

## Managing the service

```bash
systemctl --user status texpand.service    # Check status
journalctl --user -u texpand.service -f    # View logs
systemctl --user restart texpand.service   # Restart after config changes
systemctl --user stop texpand.service      # Stop
systemctl --user disable texpand.service   # Disable auto-start
```

## Debugging

Run texpand directly in a terminal (not via systemd) to see diagnostic output:

```bash
# Stop the service first to avoid conflicts
systemctl --user stop texpand.service

# Run in foreground — shows detected keyboards and trigger count
./texpand
```

You'll see output like:

```
texpand: monitoring 2 keyboard(s) — 35 triggers loaded
  AT Translated Set 2 keyboard
  Logitech USB Receiver
```

### Debug mode

Use `--debug` (or `-d`) for verbose output on stderr — shows config loading, trigger mode, loaded triggers, and match decisions. **`--debug` never prints the words you type.** If you need to trace buffer contents while diagnosing a correction, use `--debug-unsafe` and treat the output as sensitive:

```bash
./texpand --debug
```

### Checking what config was loaded

Run `texpand init` to see the config directory, then inspect the YAML files:

```bash
texpand init    # Shows config path, skips existing files
ls ~/.config/texpand/match/
```

### Watching events in real time

To see raw kernel input events (useful for verifying your keyboard is detected):

```bash
# List all input devices
ls -la /dev/input/event*

# Watch events from a specific device (Ctrl+C to stop)
# Requires: sudo pacman -S evtest
sudo evtest /dev/input/event0
```

### Checking clipboard operations

If triggers fire but paste wrong text, verify wl-clipboard works:

```bash
echo "test" | wl-copy
wl-paste -n    # Should print "test"
```

### Systemd logs

```bash
# Live logs
journalctl --user -u texpand.service -f

# Last 50 lines
journalctl --user -u texpand.service -n 50

# Since last boot
journalctl --user -u texpand.service -b
```

## Troubleshooting

### "No keyboard devices found"

```bash
groups  # Should include 'input'
sudo usermod -aG input $USER
# Log out and back in
```

### Keyboard stops working after monitor input changes

texpand automatically rescans `/dev/input` when keyboard event devices are
created, removed, renamed, or have permissions updated. It also performs a
periodic rescan as a fallback.

Run with debug logging if expansions stop after changing monitor input or power
cycling a monitor:

```bash
./texpand --debug
```

The logs include keyboard disconnect, reconnect, and rescan messages. If no
keyboard is reconnected, check that the new `/dev/input/event*` device is
readable by your user and that your user is still in the `input` group.

### "/dev/uinput" permission denied

The most common cause is the `uinput` kernel module not being loaded at boot:

```bash
# Ensure the module loads at boot and load it now
echo uinput | sudo tee /etc/modules-load.d/uinput.conf
sudo modprobe uinput

# Install the udev rule and reload
sudo cp 99-uinput.rules /etc/udev/rules.d/99-uinput.rules
sudo udevadm control --reload-rules && sudo udevadm trigger

# If udevadm trigger fails with "No such device", fix permissions manually:
sudo chgrp input /dev/uinput && sudo chmod 0660 /dev/uinput
ls -la /dev/uinput  # Should show crw-rw---- root input
```

### WAYLAND_DISPLAY not set

texpand auto-detects the Wayland socket at startup. If it fails:

```bash
systemctl --user import-environment WAYLAND_DISPLAY
systemctl --user restart texpand.service
```

### Wrong characters

The keymap assumes US/International layout. Letters and numbers work across layouts, but symbol keys (`]`, `}`, `~`, `'`) may differ.

### Autocorrection never fires

```bash
texpand autocorrect status       # enabled? dictionary loaded?
pacman -Q hunspell-pl            # dictionary installed?
journalctl --user -u texpand.service | grep -i -E "dictionary|autocorrect"
```

Common causes: dictionary still loading (first start takes a few seconds,
then it's cached), autocorrect toggled off (`ctrl+alt+slash` flips it),
the focused app is on the exclusion list (`autocorrect status` shows the
detected app), or the word is ambiguous/already valid — safe mode only
corrects unambiguous words. If status reports `dictionary: failed (...)`, fix
the reported path/package problem and run `texpand autocorrect enable` to
retry immediately.

### Corrections come out with wrong characters (e.g. `¿` or plain ASCII)

The uinput backend assumes your active layout is **Polish (Programmer)**
(`ż` = AltGr+Z etc.). On any other layout set `output: wtype` in the
`autocorrect:` config section (requires `pacman -S wtype`), or disable
autocorrection.

### App exclusions don't work / status shows "(unknown)" app

Active-window tracking needs KDE Plasma (KWin scripting over DBus). Check
`journalctl --user -u texpand.service` for "active-window detection
unavailable". On other compositors exclusions degrade to the
`on_unknown_app` policy — set it to `skip` if you'd rather have no
corrections than corrections in the wrong window.

### Dictionary cache issues

The cache self-invalidates when the dictionary changes; to force a rebuild:

```bash
rm ~/.cache/texpand/pl-index.cache
systemctl --user restart texpand.service
```

## Security & privacy

texpand reads raw keyboard events, so treat it like the security-sensitive
software it is:

- **Fully local.** No network access, no analytics, no cloud calls — the
  binary contains no networking code. The dictionary comes from your
  installed `hunspell-pl` package.
- **Nothing you type is stored.** The word buffer is a few dozen bytes of
  RAM, overwritten as you type. The dictionary cache is derived from the
  public dictionary only. `--debug` never logs typed words; only the
  clearly named `--debug-unsafe` flag can.
- **`input` group membership is a real trade-off.** Being in `input`
  means *every* process running as your user could read all keyboards,
  not just texpand. That is how non-root evdev access works on Linux
  today. `systemd-logind`'s `TakeDevice` API was evaluated as an
  alternative, but logind only grants devices to the session controller
  (your compositor), so a background service can't use it — see
  `docs/architecture.md`. Keep your user account clean and review what
  you run.
- **The udev rule is minimal**: it only sets group `input`, mode `0660`
  on `/dev/uinput`. The daemon never runs as root.
- **Clipboard fallback is off by default** because it would place
  corrected text (and briefly, your previous clipboard) into the Wayland
  clipboard, which is visible to other apps and fragile in password
  fields and remote sessions.
- **Password fields are not detected** — no Wayland API exposes that
  safely. Rely on the app exclusion list; password managers' windows can
  be added to `excluded_apps`.

## License

MIT
