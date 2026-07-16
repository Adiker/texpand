// Package fold implements Polish-specific ASCII folding.
//
// Generic Unicode accent stripping is deliberately not used: ł/Ł carry a
// stroke, not a combining accent, and are left untouched by NFD-based
// strategies. The mapping here is explicit and covers exactly the nine
// Polish diacritic letters in both cases.
package fold

var polishToASCII = map[rune]rune{
	'ą': 'a', 'ć': 'c', 'ę': 'e', 'ł': 'l', 'ń': 'n',
	'ó': 'o', 'ś': 's', 'ź': 'z', 'ż': 'z',
	'Ą': 'A', 'Ć': 'C', 'Ę': 'E', 'Ł': 'L', 'Ń': 'N',
	'Ó': 'O', 'Ś': 'S', 'Ź': 'Z', 'Ż': 'Z',
}

// Fold returns s with Polish diacritic letters replaced by their ASCII
// counterparts. Other runes are returned unchanged.
func Fold(s string) string {
	// Fast path: no diacritics, no allocation.
	if !HasDiacritics(s) {
		return s
	}
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if a, ok := polishToASCII[r]; ok {
			r = a
		}
		out = append(out, r)
	}
	return string(out)
}

// HasDiacritics reports whether s contains any Polish diacritic letter.
func HasDiacritics(s string) bool {
	for _, r := range s {
		if _, ok := polishToASCII[r]; ok {
			return true
		}
	}
	return false
}

// IsPolishLetter reports whether r is an ASCII letter or a Polish
// diacritic letter.
func IsPolishLetter(r rune) bool {
	if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
		return true
	}
	_, ok := polishToASCII[r]
	return ok
}

// IsASCIILetters reports whether s consists solely of ASCII letters.
func IsASCIILetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}
	return true
}

// IsPolishLetters reports whether s consists solely of ASCII letters and
// Polish diacritic letters.
func IsPolishLetters(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !IsPolishLetter(r) {
			return false
		}
	}
	return true
}
