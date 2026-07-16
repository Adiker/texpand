package main

import (
	"fmt"

	"github.com/andresousadotpt/texpand/internal/appfilter"
	"github.com/andresousadotpt/texpand/internal/keymap"
)

// CorrectOnConfig selects which word boundaries trigger correction.
// Pointers distinguish "unset" (use default) from an explicit false.
type CorrectOnConfig struct {
	Space       *bool `yaml:"space"`       // default true
	Enter       *bool `yaml:"enter"`       // default false (submits in chats/forms)
	Tab         *bool `yaml:"tab"`         // default false (moves focus)
	Punctuation *bool `yaml:"punctuation"` // . , ! ? : ;  default true
	Closers     *bool `yaml:"closers"`     // ) ] } "      default true
}

// AutocorrectConfig is the `autocorrect:` section of config.yml.
type AutocorrectConfig struct {
	Enabled                *bool           `yaml:"enabled"`         // default true
	Mode                   string          `yaml:"mode"`            // "safe"
	Dictionary             string          `yaml:"dictionary"`      // "auto" or /path/to/pl.dic
	MinWordLength          int             `yaml:"min_word_length"` // default 2
	CorrectOn              CorrectOnConfig `yaml:"correct_on"`
	Undo                   *bool           `yaml:"undo"`            // default true
	ToggleShortcut         *string         `yaml:"toggle_shortcut"` // default ctrl+alt+slash; "" disables
	NotifyOnToggle         *bool           `yaml:"notify_on_toggle"`
	Output                 string          `yaml:"output"` // auto | uinput | wtype
	AllowClipboardFallback bool            `yaml:"allow_clipboard_fallback"`
	Cache                  *bool           `yaml:"cache"`          // default true
	OnUnknownApp           string          `yaml:"on_unknown_app"` // correct | skip
	ExcludedApps           []string        `yaml:"excluded_apps"`  // nil → defaults
}

func boolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Normalized returns the effective values after applying defaults, or an
// error for invalid settings.
func (a *AutocorrectConfig) Normalized() (AutocorrectSettings, error) {
	s := AutocorrectSettings{
		Enabled:                boolDefault(a.Enabled, true),
		Dictionary:             a.Dictionary,
		MinWordLength:          a.MinWordLength,
		OnSpace:                boolDefault(a.CorrectOn.Space, true),
		OnEnter:                boolDefault(a.CorrectOn.Enter, false),
		OnTab:                  boolDefault(a.CorrectOn.Tab, false),
		OnPunct:                boolDefault(a.CorrectOn.Punctuation, true),
		OnClosers:              boolDefault(a.CorrectOn.Closers, true),
		Undo:                   boolDefault(a.Undo, true),
		NotifyOnToggle:         boolDefault(a.NotifyOnToggle, true),
		Output:                 a.Output,
		AllowClipboardFallback: a.AllowClipboardFallback,
		Cache:                  boolDefault(a.Cache, true),
		ExcludedApps:           a.ExcludedApps,
	}
	switch a.Mode {
	case "", "safe":
	default:
		return s, fmt.Errorf("autocorrect.mode: unsupported mode %q (only \"safe\" is implemented)", a.Mode)
	}
	if s.Dictionary == "" {
		s.Dictionary = "auto"
	}
	if s.MinWordLength == 0 {
		s.MinWordLength = 2
	}
	if s.MinWordLength < 1 || s.MinWordLength > 32 {
		return s, fmt.Errorf("autocorrect.min_word_length: %d out of range [1,32]", s.MinWordLength)
	}
	switch s.Output {
	case "":
		s.Output = "auto"
	case "auto", "uinput", "wtype":
	default:
		return s, fmt.Errorf("autocorrect.output: unknown backend %q (use auto|uinput|wtype)", s.Output)
	}
	switch a.OnUnknownApp {
	case "", "correct":
		s.CorrectOnUnknownApp = true
	case "skip":
		s.CorrectOnUnknownApp = false
	default:
		return s, fmt.Errorf("autocorrect.on_unknown_app: %q (use correct|skip)", a.OnUnknownApp)
	}
	shortcut := "ctrl+alt+slash"
	if a.ToggleShortcut != nil {
		shortcut = *a.ToggleShortcut
	}
	sc, err := keymap.ParseShortcut(shortcut)
	if err != nil {
		return s, fmt.Errorf("autocorrect.toggle_shortcut: %w", err)
	}
	s.Toggle = sc
	if s.ExcludedApps == nil {
		s.ExcludedApps = appfilter.DefaultExcludedApps
	}
	return s, nil
}

// AutocorrectSettings are the resolved, validated settings.
type AutocorrectSettings struct {
	Enabled                bool
	Dictionary             string
	MinWordLength          int
	OnSpace                bool
	OnEnter                bool
	OnTab                  bool
	OnPunct                bool
	OnClosers              bool
	Undo                   bool
	NotifyOnToggle         bool
	Output                 string
	AllowClipboardFallback bool
	Cache                  bool
	CorrectOnUnknownApp    bool
	Toggle                 keymap.Shortcut
	ExcludedApps           []string
}
