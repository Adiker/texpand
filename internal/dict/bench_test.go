package dict

import (
	"testing"
)

// Benchmarks run against the real installed pl_PL dictionary (hunspell-pl)
// and are skipped when it is absent. They measure the operations performed
// at a word boundary — never per keystroke.

func benchIndex(b *testing.B) *Index {
	b.Helper()
	dic, aff, err := Locate("auto")
	if err != nil {
		b.Skip("no system Polish dictionary installed")
	}
	ix, err := Build(dic, aff)
	if err != nil {
		b.Fatal(err)
	}
	return ix
}

func BenchmarkLookupUniqueCandidate(b *testing.B) {
	ix := benchIndex(b)
	buf := make([]string, 0, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		buf = ix.Candidates("zolw", buf)
		if len(buf) != 1 {
			b.Fatalf("candidates = %v", buf)
		}
	}
}

func BenchmarkLookupAmbiguous(b *testing.B) {
	ix := benchIndex(b)
	buf := make([]string, 0, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		buf = ix.Candidates("zle", buf)
	}
}

func BenchmarkLookupMiss(b *testing.B) {
	ix := benchIndex(b)
	buf := make([]string, 0, 8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf = buf[:0]
		buf = ix.Candidates("qwertyuiop", buf)
	}
}

func BenchmarkIsWord(b *testing.B) {
	ix := benchIndex(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ix.IsWord("pisze")
	}
}

// BenchmarkFullBoundaryDecision simulates the complete dictionary work for
// one committed word: membership check plus candidate lookup.
func BenchmarkFullBoundaryDecision(b *testing.B) {
	ix := benchIndex(b)
	buf := make([]string, 0, 8)
	words := []string{"zolw", "zrodlo", "pisze", "laska", "qwerty", "wlasnie"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := words[i%len(words)]
		if !ix.IsWord(w) {
			buf = buf[:0]
			buf = ix.Candidates(w, buf)
		}
	}
}

func BenchmarkBuildRealDictionary(b *testing.B) {
	dic, aff, err := Locate("auto")
	if err != nil {
		b.Skip("no system Polish dictionary installed")
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Build(dic, aff); err != nil {
			b.Fatal(err)
		}
	}
}
