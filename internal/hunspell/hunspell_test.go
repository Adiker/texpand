package hunspell

import (
	"slices"
	"strings"
	"testing"
)

const testAff = `SET UTF-8
TRY aioeznrwcy

PFX b Y 1
PFX b   0         nie       .

SFX a Y 4
SFX a   e         ych       [^i]e
SFX a   e         ym        [^i]e
SFX a   ie        ach       [km]anie
SFX a   ie        y         [^km]anie

SFX c N 1
SFX c   0         m         .
`

func parse(t *testing.T, aff string) *AffixSet {
	t.Helper()
	a, err := ParseAff(strings.NewReader(aff))
	if err != nil {
		t.Fatalf("ParseAff: %v", err)
	}
	return a
}

func expand(a *AffixSet, e DicEntry) []string {
	var out []string
	a.Expand(e, func(s string) { out = append(out, s) })
	slices.Sort(out)
	return slices.Compact(out)
}

func TestParseDicLine(t *testing.T) {
	cases := []struct {
		line  string
		word  string
		flags []string
		ok    bool
	}{
		{"żółw/ab", "żółw", []string{"a", "b"}, true},
		{"zolw", "zolw", nil, true},
		{"347912", "", nil, false}, // count line
		{"", "", nil, false},
		{"# comment", "", nil, false},
		{"word/ab\tpo:noun", "word", []string{"a", "b"}, true},
		{"/ab", "", nil, false}, // malformed: empty word
		{"Aaron/NOTos", "Aaron", []string{"N", "O", "T", "o", "s"}, true},
	}
	for _, c := range cases {
		e, ok := ParseDicLine(c.line, "char")
		if ok != c.ok {
			t.Errorf("ParseDicLine(%q) ok = %v, want %v", c.line, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if e.Word != c.word || !slices.Equal(e.Flags, c.flags) {
			t.Errorf("ParseDicLine(%q) = %q %v, want %q %v", c.line, e.Word, e.Flags, c.word, c.flags)
		}
	}
}

func TestParseFlagsModes(t *testing.T) {
	if got := parseFlags("AaBb", "long"); !slices.Equal(got, []string{"Aa", "Bb"}) {
		t.Errorf("long flags = %v", got)
	}
	if got := parseFlags("101,202", "num"); !slices.Equal(got, []string{"101", "202"}) {
		t.Errorf("num flags = %v", got)
	}
	if got := parseFlags("żx", "char"); !slices.Equal(got, []string{"ż", "x"}) {
		t.Errorf("char flags = %v", got)
	}
}

func TestExpandSuffix(t *testing.T) {
	a := parse(t, testAff)
	// "dobre" ends in [^i]e → strip "e", add "ych"/"ym".
	got := expand(a, DicEntry{Word: "dobre", Flags: []string{"a"}})
	want := []string{"dobre", "dobrych", "dobrym"}
	if !slices.Equal(got, want) {
		t.Errorf("expand(dobre/a) = %v, want %v", got, want)
	}
}

func TestExpandConditionClasses(t *testing.T) {
	a := parse(t, testAff)
	// "kanie" matches [km]anie → strip "ie", add "ach"; [^i]e fails (i before e).
	got := expand(a, DicEntry{Word: "kanie", Flags: []string{"a"}})
	want := []string{"kanach", "kanie"}
	if !slices.Equal(got, want) {
		t.Errorf("expand(kanie/a) = %v, want %v", got, want)
	}
	// "granie" matches [^km]anie → strip "ie", add "y".
	got = expand(a, DicEntry{Word: "granie", Flags: []string{"a"}})
	want = []string{"granie", "grany"}
	if !slices.Equal(got, want) {
		t.Errorf("expand(granie/a) = %v, want %v", got, want)
	}
}

func TestExpandPrefixAndCross(t *testing.T) {
	a := parse(t, testAff)
	// Prefix alone plus cross-products with suffix group a (both Y).
	got := expand(a, DicEntry{Word: "dobre", Flags: []string{"a", "b"}})
	want := []string{"dobre", "dobrych", "dobrym", "niedobre", "niedobrych", "niedobrym"}
	if !slices.Equal(got, want) {
		t.Errorf("expand(dobre/ab) = %v, want %v", got, want)
	}
}

func TestExpandNoCrossProduct(t *testing.T) {
	a := parse(t, testAff)
	// Group c is cross=N: no nie+m combination.
	got := expand(a, DicEntry{Word: "gra", Flags: []string{"b", "c"}})
	want := []string{"gra", "gram", "niegra"}
	if !slices.Equal(got, want) {
		t.Errorf("expand(gra/bc) = %v, want %v", got, want)
	}
}

func TestExpandUnknownFlag(t *testing.T) {
	a := parse(t, testAff)
	got := expand(a, DicEntry{Word: "słowo", Flags: []string{"Z"}})
	if !slices.Equal(got, []string{"słowo"}) {
		t.Errorf("expand with unknown flag = %v, want just the stem", got)
	}
}

func TestMalformedAffLines(t *testing.T) {
	a := parse(t, `SET UTF-8
SFX q Y 3
SFX q
SFX q e
SFX q   e         ych       [^ie
garbage line
SFX ok Y 1
SFX ok  0         s         .
`)
	// Group q is entirely malformed (missing fields, unterminated class);
	// group ok survives.
	got := expand(a, DicEntry{Word: "word", Flags: []string{"q", "ok"}})
	want := []string{"word", "words"}
	if !slices.Equal(got, want) {
		t.Errorf("expand after malformed lines = %v, want %v", got, want)
	}
}

func TestForbiddenAndCompoundFlags(t *testing.T) {
	a := parse(t, `SET UTF-8
FORBIDDENWORD !
ONLYINCOMPOUND c
NEEDAFFIX n
SFX s Y 1
SFX s   0         y         .
`)
	if got := expand(a, DicEntry{Word: "zakazane", Flags: []string{"!"}}); got != nil {
		t.Errorf("forbidden word expanded to %v, want nothing", got)
	}
	if got := expand(a, DicEntry{Word: "czlon", Flags: []string{"c"}}); got != nil {
		t.Errorf("compound-only word expanded to %v, want nothing", got)
	}
	// NEEDAFFIX: stem itself suppressed, affixed forms kept.
	got := expand(a, DicEntry{Word: "stem", Flags: []string{"n", "s"}})
	if !slices.Equal(got, []string{"stemy"}) {
		t.Errorf("needaffix expansion = %v, want [stemy]", got)
	}
}

func TestStripMismatchSkipsForm(t *testing.T) {
	a := parse(t, `SET UTF-8
SFX s Y 1
SFX s   x         y         .
`)
	// Condition "." matches but strip "x" is absent → form skipped, no panic.
	got := expand(a, DicEntry{Word: "word", Flags: []string{"s"}})
	if !slices.Equal(got, []string{"word"}) {
		t.Errorf("expand = %v, want [word]", got)
	}
}

func TestAffixContinuationFlagsStripped(t *testing.T) {
	a := parse(t, `SET UTF-8
SFX s Y 1
SFX s   0         ów/M      .
`)
	got := expand(a, DicEntry{Word: "kot", Flags: []string{"s"}})
	if !slices.Equal(got, []string{"kot", "kotów"}) {
		t.Errorf("expand = %v, want [kot kotów]", got)
	}
}
