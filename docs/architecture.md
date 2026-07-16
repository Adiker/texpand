# texpand architecture (with Polish autocorrection)

This document records the architecture decisions made when extending texpand
with system-wide Polish diacritics autocorrection. It reflects the audit of
the original codebase and the design of the new subsystem.

## Audit summary (original codebase)

The original texpand is a small Go daemon:

- `keyboard.go` — enumerates `/dev/input/event*` via evdev, opens every device
  that reports both `KEY_A` and `KEY_ENTER`, and runs one reader goroutine per
  keyboard. Events are funneled into a single buffered channel. The daemon's
  own uinput device (named `texpand`) is skipped by name, which prevents
  feedback loops. Hotplug is handled by an fsnotify watch on `/dev/input`
  plus a 5-second rescan ticker.
- `expander.go` — a rolling byte buffer fed by a US-layout keycode→char map,
  matched against configured triggers on space (or every key in `immediate`
  mode). Output is typed through uinput key events; text that cannot be
  produced with US-layout keycodes falls back to `wtype`, then to a
  clipboard paste.
- `config.go` / `migrate.go` — espanso-style YAML config with hot reload and
  a versioned migration system.
- The event loop (in `main.go`) is a single goroutine `select`; after an
  expansion it sleeps 5 ms and drains the channel so stale physical events
  do not re-trigger.

Audit conclusions:

- The evdev→channel→single-consumer design is sound and already almost
  latency-free on the ordinary-typing path (a map lookup and a string
  append). We keep it and hang the autocorrector off the same loop.
- There were **no tests**. The expander logic was moved behind interfaces so
  it, and everything new, can be tested with synthetic events.
- Modifier handling was incomplete for our purposes: only Shift was tracked
  as *state*; Ctrl/Alt/Meta merely reset the buffer on key-down, so `Ctrl+C`
  would leave subsequent keys unguarded if Ctrl was held. Caps Lock was not
  tracked at all. The new corrector tracks full modifier state; the original
  expander behaviour is unchanged.
- Licensing: upstream is MIT. Dependencies: bendahl/uinput (MIT),
  holoplot/go-evdev (MIT), fsnotify (BSD-3), yaml.v3 (MIT/Apache-2.0),
  godbus/dbus (BSD-2, added by this work) — all MIT-compatible.
  `radislabus-star/lay-public` was **not** used as a code source; no code was
  copied or derived from it.

## Decision: extend, do not rewrite

The existing daemon already solves device monitoring, hotplug, uinput
output, config loading/migration and systemd integration. Rewriting would
re-create all of that for no benefit. The autocorrection subsystem is added
as separate `internal/` packages with narrow interfaces; `main.go` only
wires them together.

## Package layout

```
texpand/                  package main — wiring, event loop, CLI
├── internal/fold         Polish-specific ASCII folding (explicit table)
├── internal/hunspell     .aff/.dic parsing and affix expansion (unmunch)
├── internal/dict         compact in-memory index + optional cache
├── internal/correct      word-buffer state machine, candidate selection,
│                         case preservation, undo (pure, no I/O)
├── internal/output       output backends: uinput (Polish Programmer AltGr),
│                         wtype, clipboard; fallback chain
├── internal/appfilter    active-application detection (KWin/DBus), exclusions
└── internal/control      session-bus control interface + CLI client,
                          desktop notifications
```

`internal/correct` is deliberately free of evdev, uinput, and file I/O: it
consumes `(keycode, value)` pairs and returns a *plan* (`backspaces N, type
S`). This is what makes the whole pipeline unit-testable with synthetic
events.

## Event flow

```
evdev readers (1 goroutine per keyboard)
        │  KeyEvent{device,code,value}
        ▼
   buffered channel (cap 64)
        ▼
 per-device modifier/Caps aggregator
        │  effective modifier snapshot
        ▼
 main select loop ── expander.HandleEvent   (existing text expansion)
        │
        └───────── corrector.HandleEvent    (new)
                        │ *Plan (only at word boundaries)
                        ▼
                  output backend chain (uinput → wtype → [clipboard])
```

- Ordinary keystrokes only update in-memory state (a few branches, one map
  lookup, one rune append). **No dictionary access, no allocation beyond the
  buffer, no I/O.** This is verified by benchmarks.
- Dictionary lookup happens only when a separator key commits a word, and it
  is a binary search over an in-memory index (sub-microsecond).
- Corrections are executed on the main loop goroutine, so they are naturally
  serialized; reader goroutines never block on them (the channel buffers).
  The daemon's uinput device is never monitored, so queued events can only
  come from physical keyboards and are processed normally after injected
  output instead of being discarded.
- If the expander fires, the corrector's buffer is invalidated (and vice
  versa) so the two subsystems cannot both rewrite the same text.

## Dictionary pipeline

1. Locate `pl_PL.dic`/`pl_PL.aff` (config `dictionary: auto` probes
   `/usr/share/hunspell` and `/usr/share/myspell/dicts`; a path can be given
   explicitly).
2. Parse the `.aff` file. `pl_PL` uses only `SET`, `TRY`, `PFX`, `SFX`,
   `REP`, `MAP` with single-character flags, but the parser also understands
   `FLAG long/num`, and skips unknown directives and malformed lines instead
   of failing.
3. "Unmunch": every `.dic` stem is expanded with its matching `SFX` rules,
   `PFX` rules, and cross-product `PFX`×`SFX` combinations. Conditions
   (literal chars and `[...]`/`[^...]` classes) are compiled once per rule.
4. Every generated form is lowercased and classified:
   - pure ASCII letters → membership set (used for "already a valid word"),
   - letters with Polish diacritics → candidate entry `fold(w) → w`,
   - anything else (digits, foreign accents) → ignored.
5. Both sets are stored as *sorted string blobs with offset tables* and
   queried by binary search. This keeps millions of forms in tens of MB and
   avoids Go map overhead. The index is immutable after build and safe for
   concurrent readers.

Loading runs **once, in a background goroutine at startup**; until it
finishes, the corrector simply produces no corrections. The key-event path
never touches the dictionary files.

An optional cache (`~/.cache/texpand/pl-index.cache`, config `cache: true`)
stores the finished index; it is validated against the absolute paths,
sizes, and mtimes of both source files and rebuilt on any mismatch. The
program works with the cache disabled or deleted.

## Safe correction mode

A word is corrected only when **all** hold:

1. the typed token consists purely of letters and contains no diacritics
   (tokens with digits, hyphens, apostrophes, underscores or slashes are
   never corrected),
2. its lowercase form is **not** already a dictionary word,
3. the folded index yields exactly **one** distinct candidate,
4. the candidate differs from the typed word (guaranteed by construction,
   still checked),
5. no Ctrl/Alt/Meta modifier is held, the buffer was not invalidated by
   cursor movement/editing, autocorrect is enabled, and the active
   application is not excluded,
6. the token length is within `[min_word_length, 32]`.

So `zolw→żółw`, `zrodlo→źródło`, but `pisze` and `laska` stay untouched
(they are themselves valid words), and `zle` stays untouched (two
candidates: `złe`, `źle`).

## Word boundaries

Separator keys commit the word. Defaults (all configurable):

| group        | keys                          | default |
|--------------|-------------------------------|---------|
| space        | Space                         | on      |
| punctuation  | `.` `,` `!` `?` `:` `;`       | on      |
| closers      | `)` `]` `}` `"`               | on      |
| enter        | Enter / keypad Enter          | **off** |
| tab          | Tab                           | **off** |

Enter and Tab are recognized boundaries but default to *commit without
correcting*: Enter frequently submits (chat, forms) and Tab moves focus, so
retyping after the fact could edit the wrong widget or send a second
message. This is a deliberate, documented deviation from "correct on every
boundary" in favour of the spec's own "the default mode must be
conservative". Both can be enabled in config.

Hyphens and apostrophes join the token but mark it impure, so
`biało-czerwony` typed as `bialo-czerwony` and `O'Brien` are never touched.
Opening brackets/quotes reset the buffer, so `"zolw"` and `(zolw)` still
correct the inner word. The replacement always re-types the exact separator
character that triggered it.

## Case preservation

The case pattern of the typed token is detected and re-applied to the
(lowercase-normalized) candidate:

- all-lower → lower (`zolw→żółw`)
- first-upper (incl. single letter) → first-upper (`Zolw→Żółw`)
- all-upper, length ≥ 2 → upper (`ZOLW→ŻÓŁW`)
- any other mixed pattern → **skip correction** (deterministic, tested)

Caps Lock and held modifiers are aggregated per evdev device (initial Caps
state is read from the keyboard LED at startup and on hotplug). Releasing one
Shift does not clear another Shift still held on the same or another keyboard.
The uinput backend compensates for active Caps Lock when choosing whether to
emit Shift, so `ZOLW` typed with Caps Lock also outputs uppercase correctly.

## Output strategy

`internal/output.Backend` is a small interface (`Type(text)`,
`Backspace(n)`). Backends:

1. **uinput / Polish Programmer** (primary): ASCII via the US reverse map,
   Polish diacritics via AltGr(+Shift) combinations (`ż`=AltGr+Z,
   `ź`=AltGr+X, …). Fastest path, no subprocess, works because KDE applies
   the user's layout to the virtual keyboard.
2. **wtype** (fallback for text the uinput map cannot produce, or when
   `output: wtype` is forced). Only ever spawned during an actual
   correction, never per keystroke, and never on the reader goroutines.
3. **clipboard paste** (wl-copy + Ctrl+V), **disabled by default**
   (`allow_clipboard_fallback: false`) because it mutates the clipboard and
   misbehaves in password fields / remote sessions.

The chain tries backends in order; `output: auto` = uinput→wtype.

## Undo

Phone-style single-level undo. Immediately after an automatic correction,
one Backspace (which itself deletes the separator, as usual) makes the
daemon delete the corrected word and re-type exactly what the user had
typed. The restored word is marked *suppressed* so committing it again does
not re-correct. Any other key commits the correction and clears the undo
state.

## Application exclusions

Active-window detection on KDE Plasma Wayland: there is no stable public
"give me the active window class" DBus call, so texpand registers a tiny
KWin script at runtime (via `org.kde.kwin.Scripting`) that pushes
`resourceClass` to texpand's own session-bus name on every
`windowActivated` (Plasma 6) / `clientActivated` (Plasma 5) signal. This is
event-driven (no polling), read-only with respect to KWin, and the script
is unloaded on exit.

Matching against `excluded_apps` is case-insensitive with `*` globs.
Defaults exclude common terminals, IDEs and remote-desktop apps. If the
active application is unknown (script failed, non-KDE compositor), the
behaviour is `on_unknown_app: correct` by default — configurable to `skip`
for users who prefer never correcting in an unidentified context. If
detection is *working* and the app is excluded, nothing is corrected.

Password fields are not inspected — no supported Wayland API exposes that
safely; exclusions + the clipboard fallback being off are the mitigation.

## Runtime control

The daemon owns the session-bus name `io.github.texpand` and exports
`Enable`/`Disable`/`Toggle`/`Status` plus `SetActiveWindow` (for the KWin
script). `texpand autocorrect enable|disable|toggle|status` are thin DBus
clients — no second keyboard-monitoring process is ever started. A
configurable keyboard shortcut (default `ctrl+alt+slash`) toggles
autocorrect from the state machine itself, and toggles can raise a desktop
notification via `org.freedesktop.Notifications`.

## Security & privacy

- Fully local: no network code, no analytics, nothing persisted about
  typed text. The only files written are the config (by `init`/`migrate`)
  and the optional dictionary index cache (derived from the public
  dictionary, not from typing).
- `--debug` never prints typed words or buffer contents; word-level tracing
  requires the separate `--debug-unsafe` flag whose name states the risk.
- The daemon runs as the user, in the `input` group — membership in that
  group means *any* process of that user can read all keyboards; this is
  documented in the README. The udev rule is limited to `uinput` and mode
  0660/group input.
- systemd-logind `TakeDevice` was evaluated: it would avoid the `input`
  group, but logind hands out devices only to the session controller (one
  per session — normally the compositor), so a background service cannot
  use it on a running Plasma session without stealing the controller role.
  Conclusion: not viable for this design; the input-group approach stays,
  with its implications documented.

## Known limitations

- Output assumes the active layout is Polish (Programmer). On other layouts
  the uinput backend would emit wrong glyphs; set `output: wtype` or toggle
  off. Layout switching is not auto-detected (evdev has no layout notion).
- Correction fires on `.` also inside things like file names typed in a
  GUI field (`zolw.txt` → `żółw.txt`) — mitigated by exclusions and undo.
- Multiple keyboards share one word buffer (same as the expander), but their
  held modifiers are tracked independently. Typing one word on two keyboards
  simultaneously is still out of scope.
- Initial Caps Lock state is read from the LED; exotic keyboards without a
  Caps Lock LED start assumed-off until first press.
