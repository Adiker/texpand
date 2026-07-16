// Package hunspell implements the subset of the Hunspell dictionary format
// needed to expand ("unmunch") an inflected word list from a .dic/.aff pair.
//
// It supports SET (UTF-8 assumed), FLAG (char/long/num), PFX and SFX rules
// with cross-product combination. Directives that do not affect word-form
// generation (TRY, REP, MAP, KEY, ...) and malformed lines are skipped
// rather than treated as errors: dictionary files in the wild are messy and
// a correction daemon must not fail to start because of one bad line.
package hunspell

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// condElem is one element of a compiled affix condition: either a literal
// rune or a [..]/[^..] character class.
type condElem struct {
	isClass bool
	negated bool
	chars   string // set members for classes
	literal rune
}

func (c condElem) matches(r rune) bool {
	if !c.isClass {
		return c.literal == r
	}
	in := strings.ContainsRune(c.chars, r)
	if c.negated {
		return !in
	}
	return in
}

// affixRule is a single PFX/SFX rule line, compiled.
type affixRule struct {
	strip string
	affix string
	cond  []condElem
	cross bool
}

// AffixSet holds all affix rules parsed from an .aff file.
type AffixSet struct {
	flagMode string // "char", "long", "num"
	pfx      map[string][]affixRule
	sfx      map[string][]affixRule

	needAffix    string // flag marking stems invalid without an affix
	forbidden    string // flag marking forbidden words
	onlyCompound string // flag marking compound-only forms
}

// FlagMode returns the flag encoding declared by the .aff file.
func (a *AffixSet) FlagMode() string { return a.flagMode }

// ParseAff parses an .aff file. Unknown directives and malformed lines are
// ignored.
func ParseAff(r io.Reader) (*AffixSet, error) {
	a := &AffixSet{
		flagMode: "char",
		pfx:      make(map[string][]affixRule),
		sfx:      make(map[string][]affixRule),
	}
	// Header lines ("PFX b Y 1") precede their rule lines; remember each
	// group's cross-product setting so rules can be compiled with it.
	cross := make(map[string]bool)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || strings.HasPrefix(fields[0], "#") {
			continue
		}
		switch fields[0] {
		case "FLAG":
			if len(fields) >= 2 {
				switch fields[1] {
				case "long":
					a.flagMode = "long"
				case "num":
					a.flagMode = "num"
				}
			}
		case "NEEDAFFIX", "PSEUDOROOT":
			if len(fields) >= 2 {
				a.needAffix = fields[1]
			}
		case "FORBIDDENWORD":
			if len(fields) >= 2 {
				a.forbidden = fields[1]
			}
		case "ONLYINCOMPOUND":
			if len(fields) >= 2 {
				a.onlyCompound = fields[1]
			}
		case "PFX", "SFX":
			// Header: PFX flag Y/N count — has a numeric last field and
			// exactly 4 fields. Rule: PFX flag strip affix cond [morph...].
			if len(fields) == 4 {
				if _, err := strconv.Atoi(fields[3]); err == nil {
					cross[fields[1]] = fields[2] == "Y"
					continue // header line
				}
			}
			if len(fields) < 4 {
				continue // malformed; skip
			}
			flag := fields[1]
			rule, ok := compileRule(fields, cross[flag])
			if !ok {
				continue
			}
			if fields[0] == "PFX" {
				a.pfx[flag] = append(a.pfx[flag], rule)
			} else {
				a.sfx[flag] = append(a.sfx[flag], rule)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read .aff: %w", err)
	}
	return a, nil
}

func compileRule(fields []string, cross bool) (affixRule, bool) {
	strip, affix, cond := fields[2], fields[3], "."
	if len(fields) >= 5 {
		cond = fields[4]
	}
	if strip == "0" {
		strip = ""
	}
	if affix == "0" {
		affix = ""
	}
	// Affixes may carry continuation flags after '/': strip them. pl_PL
	// does not use two-level affixation, and for unmunching the base form
	// is what matters.
	if i := strings.IndexByte(affix, '/'); i >= 0 {
		affix = affix[:i]
	}
	elems, ok := compileCond(cond)
	if !ok {
		return affixRule{}, false
	}
	return affixRule{strip: strip, affix: affix, cond: elems, cross: cross}, true
}

// compileCond compiles a Hunspell condition string into elements.
// "." compiles to nil (always matches).
func compileCond(cond string) ([]condElem, bool) {
	if cond == "." {
		return nil, true
	}
	var elems []condElem
	runes := []rune(cond)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '[' {
			j := i + 1
			neg := false
			if j < len(runes) && runes[j] == '^' {
				neg = true
				j++
			}
			start := j
			for j < len(runes) && runes[j] != ']' {
				j++
			}
			if j >= len(runes) {
				return nil, false // unterminated class
			}
			elems = append(elems, condElem{isClass: true, negated: neg, chars: string(runes[start:j])})
			i = j
		} else if r == '.' {
			elems = append(elems, condElem{isClass: true, negated: true, chars: ""})
		} else {
			elems = append(elems, condElem{literal: r})
		}
	}
	return elems, true
}

// sfxMatches reports whether a suffix rule's condition matches word (given
// as runes).
func sfxMatches(rule *affixRule, word []rune) bool {
	n := len(rule.cond)
	if n == 0 {
		return true
	}
	if len(word) < n {
		return false
	}
	tail := word[len(word)-n:]
	for i, c := range rule.cond {
		if !c.matches(tail[i]) {
			return false
		}
	}
	return true
}

// pfxMatches reports whether a prefix rule's condition matches word.
func pfxMatches(rule *affixRule, word []rune) bool {
	n := len(rule.cond)
	if n == 0 {
		return true
	}
	if len(word) < n {
		return false
	}
	for i, c := range rule.cond {
		if !c.matches(word[i]) {
			return false
		}
	}
	return true
}

func applySfx(rule *affixRule, word string) (string, bool) {
	if rule.strip != "" {
		if !strings.HasSuffix(word, rule.strip) {
			return "", false
		}
		word = word[:len(word)-len(rule.strip)]
	}
	return word + rule.affix, true
}

func applyPfx(rule *affixRule, word string) (string, bool) {
	if rule.strip != "" {
		if !strings.HasPrefix(word, rule.strip) {
			return "", false
		}
		word = word[len(rule.strip):]
	}
	return rule.affix + word, true
}

// DicEntry is one parsed .dic line.
type DicEntry struct {
	Word  string
	Flags []string
}

// ParseDicLine parses a single .dic line into a word and its flags using
// the given flag mode. Returns ok=false for lines that should be skipped
// (empty, comments, counts, malformed).
func ParseDicLine(line, flagMode string) (DicEntry, bool) {
	line = strings.TrimRight(line, "\r\n")
	if line == "" || strings.HasPrefix(line, "#") {
		return DicEntry{}, false
	}
	// Morphological fields follow a tab.
	if i := strings.IndexByte(line, '\t'); i >= 0 {
		line = line[:i]
	}
	// The first line of a .dic is the entry count; pure numbers anywhere
	// are not words we care about.
	if _, err := strconv.Atoi(line); err == nil {
		return DicEntry{}, false
	}
	word, flagStr := line, ""
	if i := strings.LastIndexByte(line, '/'); i >= 0 {
		word, flagStr = line[:i], line[i+1:]
	}
	if word == "" {
		return DicEntry{}, false
	}
	return DicEntry{Word: word, Flags: parseFlags(flagStr, flagMode)}, true
}

func parseFlags(s, mode string) []string {
	if s == "" {
		return nil
	}
	switch mode {
	case "num":
		return strings.Split(s, ",")
	case "long":
		var flags []string
		r := []rune(s)
		for i := 0; i+1 < len(r); i += 2 {
			flags = append(flags, string(r[i:i+2]))
		}
		return flags
	default: // char
		var flags []string
		for _, r := range s {
			flags = append(flags, string(r))
		}
		return flags
	}
}

// Expand generates every surface form of a .dic entry (including the stem
// itself, unless marked NEEDAFFIX) and calls emit for each. Forms marked
// FORBIDDENWORD or ONLYINCOMPOUND are not emitted.
func (a *AffixSet) Expand(e DicEntry, emit func(string)) {
	for _, f := range e.Flags {
		if f == a.forbidden && a.forbidden != "" {
			return
		}
	}
	onlyCompound := false
	needAffix := false
	for _, f := range e.Flags {
		if a.onlyCompound != "" && f == a.onlyCompound {
			onlyCompound = true
		}
		if a.needAffix != "" && f == a.needAffix {
			needAffix = true
		}
	}
	if !needAffix && !onlyCompound {
		emit(e.Word)
	}
	if onlyCompound {
		return
	}

	wordRunes := []rune(e.Word)

	// Collect applicable cross-product prefix rules once; they combine
	// with the stem and with every cross-product suffixed form.
	var crossPfx []*affixRule
	for _, f := range e.Flags {
		rules := a.pfx[f]
		for i := range rules {
			rule := &rules[i]
			if !pfxMatches(rule, wordRunes) {
				continue
			}
			if form, ok := applyPfx(rule, e.Word); ok {
				emit(form)
				if rule.cross {
					crossPfx = append(crossPfx, rule)
				}
			}
		}
	}

	for _, f := range e.Flags {
		rules := a.sfx[f]
		for i := range rules {
			rule := &rules[i]
			if !sfxMatches(rule, wordRunes) {
				continue
			}
			form, ok := applySfx(rule, e.Word)
			if !ok {
				continue
			}
			emit(form)
			if rule.cross {
				for _, p := range crossPfx {
					if pform, ok := applyPfx(p, form); ok {
						emit(pform)
					}
				}
			}
		}
	}
}
