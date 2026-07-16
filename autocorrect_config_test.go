package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestAutocorrectDefaults(t *testing.T) {
	var cfg AutocorrectConfig
	s, err := cfg.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Enabled || !s.OnSpace || !s.OnPunct || !s.OnClosers || !s.Undo || !s.Cache {
		t.Errorf("defaults wrong: %+v", s)
	}
	if s.OnEnter || s.OnTab {
		t.Error("enter/tab correction must default to off")
	}
	if s.AllowClipboardFallback {
		t.Error("clipboard fallback must default to off")
	}
	if s.MinWordLength != 2 || s.Dictionary != "auto" || s.Output != "auto" {
		t.Errorf("defaults wrong: %+v", s)
	}
	if !s.CorrectOnUnknownApp {
		t.Error("on_unknown_app should default to correct")
	}
	if s.Toggle.IsZero() {
		t.Error("default toggle shortcut missing")
	}
	if len(s.ExcludedApps) == 0 {
		t.Error("default exclusions missing")
	}
}

func TestAutocorrectYAMLOverrides(t *testing.T) {
	src := `
enabled: false
mode: safe
dictionary: /usr/share/hunspell/pl_PL.dic
min_word_length: 4
correct_on:
  space: false
  enter: true
undo: false
toggle_shortcut: "meta+f9"
output: wtype
allow_clipboard_fallback: true
on_unknown_app: skip
excluded_apps: [myterm]
`
	var cfg AutocorrectConfig
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatal(err)
	}
	s, err := cfg.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if s.Enabled || s.OnSpace || !s.OnEnter || s.Undo {
		t.Errorf("overrides not applied: %+v", s)
	}
	if s.MinWordLength != 4 || s.Output != "wtype" || !s.AllowClipboardFallback {
		t.Errorf("overrides not applied: %+v", s)
	}
	if s.CorrectOnUnknownApp {
		t.Error("on_unknown_app: skip not applied")
	}
	if !s.Toggle.Meta || s.Toggle.Ctrl {
		t.Errorf("toggle shortcut: %+v", s.Toggle)
	}
	if len(s.ExcludedApps) != 1 || s.ExcludedApps[0] != "myterm" {
		t.Errorf("excluded_apps: %v", s.ExcludedApps)
	}
	// Punctuation was not mentioned → stays default true.
	if !s.OnPunct {
		t.Error("unset correct_on.punctuation lost its default")
	}
}

// The config file shipped by `texpand init` must parse and normalize to
// the documented defaults.
func TestShippedDefaultConfigValid(t *testing.T) {
	var cfg AppConfig
	if err := yaml.Unmarshal(defaultAppConfig, &cfg); err != nil {
		t.Fatalf("defaults/config.yml does not parse: %v", err)
	}
	s, err := cfg.Autocorrect.Normalized()
	if err != nil {
		t.Fatalf("defaults/config.yml invalid: %v", err)
	}
	if !s.Enabled || !s.OnSpace || s.OnEnter || s.OnTab || !s.Undo || s.AllowClipboardFallback {
		t.Errorf("shipped defaults diverge from documented behaviour: %+v", s)
	}
}

func TestAutocorrectValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*AutocorrectConfig)
		want string
	}{
		{"bad mode", func(c *AutocorrectConfig) { c.Mode = "aggressive" }, "mode"},
		{"bad output", func(c *AutocorrectConfig) { c.Output = "xdotool" }, "output"},
		{"bad unknown-app", func(c *AutocorrectConfig) { c.OnUnknownApp = "maybe" }, "on_unknown_app"},
		{"bad shortcut key", func(c *AutocorrectConfig) { s := "ctrl+alt+nosuch"; c.ToggleShortcut = &s }, "toggle_shortcut"},
		{"shortcut without modifier", func(c *AutocorrectConfig) { s := "z"; c.ToggleShortcut = &s }, "toggle_shortcut"},
		{"min length too large", func(c *AutocorrectConfig) { c.MinWordLength = 100 }, "min_word_length"},
	}
	for _, c := range cases {
		var cfg AutocorrectConfig
		c.mut(&cfg)
		if _, err := cfg.Normalized(); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want mention of %q", c.name, err, c.want)
		}
	}
	// Empty shortcut string disables the shortcut without error.
	var cfg AutocorrectConfig
	empty := ""
	cfg.ToggleShortcut = &empty
	s, err := cfg.Normalized()
	if err != nil {
		t.Fatal(err)
	}
	if !s.Toggle.IsZero() {
		t.Error("empty toggle_shortcut should disable the shortcut")
	}
}
