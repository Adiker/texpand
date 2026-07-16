package dict

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// writeDict writes a small .dic/.aff pair and returns their paths.
func writeDict(t *testing.T, dic, aff string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	dicPath := filepath.Join(dir, "test.dic")
	affPath := filepath.Join(dir, "test.aff")
	if err := os.WriteFile(dicPath, []byte(dic), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(affPath, []byte(aff), 0644); err != nil {
		t.Fatal(err)
	}
	return dicPath, affPath
}

const testAff = `SET UTF-8

SFX s Y 1
SFX s   0         y         .
`

const testDic = `10
żółw/s
źródło
laska
łaska
pisze
piszę
złe
źle
Kraków
malformed/line/extra
`

func buildTest(t *testing.T) *Index {
	t.Helper()
	dicPath, affPath := writeDict(t, testDic, testAff)
	ix, err := Build(dicPath, affPath)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return ix
}

func TestIsWord(t *testing.T) {
	ix := buildTest(t)
	for _, w := range []string{"laska", "pisze", "krakow"} {
		if w == "krakow" {
			// "Kraków" has diacritics; its folded form is a candidate,
			// not an ASCII word.
			if ix.IsWord(w) {
				t.Errorf("IsWord(%q) = true, want false", w)
			}
			continue
		}
		if !ix.IsWord(w) {
			t.Errorf("IsWord(%q) = false, want true", w)
		}
	}
	if ix.IsWord("zolw") {
		t.Error("IsWord(zolw) = true, want false")
	}
}

func TestCandidates(t *testing.T) {
	ix := buildTest(t)
	cases := []struct {
		key  string
		want []string
	}{
		{"zolw", []string{"żółw"}},
		{"zolwy", []string{"żółwy"}}, // affixed form żółw+y
		{"zrodlo", []string{"źródło"}},
		{"laska", []string{"łaska"}},
		{"pisze", []string{"piszę"}},
		{"zle", []string{"złe", "źle"}},
		{"krakow", []string{"kraków"}}, // normalized to lowercase
		{"nomatch", nil},
	}
	for _, c := range cases {
		got := ix.Candidates(c.key, nil)
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("Candidates(%q) = %v, want %v", c.key, got, want)
		}
	}
}

func TestCandidatesDeduplicated(t *testing.T) {
	// The same form generated twice (stem + affix collision) must appear once.
	dicPath, affPath := writeDict(t, "2\nżółw/s\nżółwy\n", testAff)
	ix, err := Build(dicPath, affPath)
	if err != nil {
		t.Fatal(err)
	}
	got := ix.Candidates("zolwy", nil)
	if !slices.Equal(got, []string{"żółwy"}) {
		t.Errorf("Candidates(zolwy) = %v, want single żółwy", got)
	}
}

func TestCandidatesNoAllocWithBuffer(t *testing.T) {
	ix := buildTest(t)
	buf := make([]string, 0, 8)
	allocs := testing.AllocsPerRun(100, func() {
		buf = buf[:0]
		buf = ix.Candidates("zle", buf)
	})
	if allocs != 0 {
		t.Errorf("Candidates with buffer allocated %v times, want 0", allocs)
	}
}

func TestMalformedDictionary(t *testing.T) {
	// Garbage lines, bad flags, empty lines: build must succeed and index
	// the valid entries.
	dicPath, affPath := writeDict(t, "not_a_count\nżółw\n\n###\n/onlyflags\n", "GARBAGE\nSFX broken\n")
	ix, err := Build(dicPath, affPath)
	if err != nil {
		t.Fatalf("Build with malformed input: %v", err)
	}
	if got := ix.Candidates("zolw", nil); !slices.Equal(got, []string{"żółw"}) {
		t.Errorf("Candidates(zolw) = %v", got)
	}
	// "not_a_count" contains an underscore → dropped entirely.
	if ix.IsWord("not_a_count") {
		t.Error("underscore token indexed as word")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dicPath, affPath := writeDict(t, testDic, testAff)
	ix, err := Build(dicPath, affPath)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "sub", "idx.cache")
	if err := SaveCache(cachePath, ix, dicPath, affPath); err != nil {
		t.Fatalf("SaveCache: %v", err)
	}
	got, err := LoadCache(cachePath, dicPath, affPath)
	if err != nil {
		t.Fatalf("LoadCache: %v", err)
	}
	aw, cp := ix.Stats()
	gaw, gcp := got.Stats()
	if aw != gaw || cp != gcp {
		t.Fatalf("stats mismatch: built (%d,%d) loaded (%d,%d)", aw, cp, gaw, gcp)
	}
	if c := got.Candidates("zle", nil); len(c) != 2 {
		t.Errorf("loaded cache Candidates(zle) = %v", c)
	}
}

func TestCacheInvalidation(t *testing.T) {
	dicPath, affPath := writeDict(t, testDic, testAff)
	ix, err := Build(dicPath, affPath)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "idx.cache")
	if err := SaveCache(cachePath, ix, dicPath, affPath); err != nil {
		t.Fatal(err)
	}

	// Change the dictionary contents (size changes) → cache must be rejected.
	if err := os.WriteFile(dicPath, []byte(testDic+"nowe\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCache(cachePath, dicPath, affPath); err == nil {
		t.Error("LoadCache accepted a stale cache")
	}

	// Corrupt file → rejected, not a panic.
	if err := os.WriteFile(cachePath, []byte("TXPDICT2garbage"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCache(cachePath, dicPath, affPath); err == nil {
		t.Error("LoadCache accepted a corrupt cache")
	}
}

func TestLocateExplicit(t *testing.T) {
	dicPath, _ := writeDict(t, testDic, testAff)
	d, a, err := Locate(dicPath)
	if err != nil {
		t.Fatal(err)
	}
	if d != dicPath || filepath.Ext(a) != ".aff" {
		t.Errorf("Locate = %q %q", d, a)
	}
	if _, _, err := Locate("/nonexistent/pl.dic"); err == nil {
		t.Error("Locate accepted a nonexistent path")
	}
}

// Real-dictionary coverage: skipped when hunspell-pl is not installed.
func realDict(tb testing.TB) *Index {
	tb.Helper()
	dic, aff, err := Locate("auto")
	if err != nil {
		tb.Skip("no system Polish dictionary installed")
	}
	ix, err := Build(dic, aff)
	if err != nil {
		tb.Fatalf("Build(%s): %v", dic, err)
	}
	return ix
}

func TestRealPolishDictionary(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	ix := realDict(t)
	aw, cp := ix.Stats()
	t.Logf("real dictionary: %d ascii words, %d candidate pairs", aw, cp)
	if aw < 100000 || cp < 100000 {
		t.Fatalf("suspiciously small index: %d/%d", aw, cp)
	}

	for _, c := range []struct {
		key  string
		want string
	}{
		{"zolw", "żółw"},
		{"zrodlo", "źródło"},
		{"wlasnie", "właśnie"},
	} {
		got := ix.Candidates(c.key, nil)
		if !slices.Contains(got, c.want) {
			t.Errorf("Candidates(%q) = %v, want to contain %q", c.key, got, c.want)
		}
		if len(got) != 1 {
			t.Errorf("Candidates(%q) = %v, want exactly one for safe correction", c.key, got)
		}
	}

	// Ambiguous / already-valid words.
	for _, w := range []string{"laska", "pisze"} {
		if !ix.IsWord(w) {
			t.Errorf("IsWord(%q) = false, want true", w)
		}
	}
	if got := ix.Candidates("zle", nil); len(got) < 2 {
		t.Errorf("Candidates(zle) = %v, want złe and źle", got)
	}
}
