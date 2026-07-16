package fold

import "testing"

func TestFold(t *testing.T) {
	cases := []struct{ in, want string }{
		{"żółw", "zolw"},
		{"źródło", "zrodlo"},
		{"ĄĆĘŁŃÓŚŹŻ", "ACELNOSZZ"},
		{"ąćęłńóśźż", "acelnoszz"},
		{"zolw", "zolw"},
		{"", ""},
		{"łaska", "laska"},
		{"piszę", "pisze"},
		{"café", "café"}, // non-Polish accents untouched
		{"Żółć123", "Zolc123"},
	}
	for _, c := range cases {
		if got := Fold(c.in); got != c.want {
			t.Errorf("Fold(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHasDiacritics(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"żółw", true},
		{"zolw", false},
		{"ł", true},
		{"Ł", true},
		{"", false},
		{"café", false}, // é is not a Polish diacritic
		{"abcĄ", true},
	}
	for _, c := range cases {
		if got := HasDiacritics(c.in); got != c.want {
			t.Errorf("HasDiacritics(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsASCIILetters(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"zolw", true},
		{"Zolw", true},
		{"żółw", false},
		{"zolw1", false},
		{"zol-w", false},
		{"zol'w", false},
		{"", false},
		{"a", true},
	}
	for _, c := range cases {
		if got := IsASCIILetters(c.in); got != c.want {
			t.Errorf("IsASCIILetters(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestIsPolishLetters(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"żółw", true},
		{"zolw", true},
		{"ŻÓŁW", true},
		{"żółw1", false},
		{"café", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsPolishLetters(c.in); got != c.want {
			t.Errorf("IsPolishLetters(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestFoldNoAllocOnASCII(t *testing.T) {
	allocs := testing.AllocsPerRun(100, func() {
		_ = Fold("zolw")
	})
	if allocs != 0 {
		t.Errorf("Fold on ASCII input allocated %v times, want 0", allocs)
	}
}
