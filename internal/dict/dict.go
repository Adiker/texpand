// Package dict builds and queries the in-memory Polish word index used by
// the autocorrector.
//
// Two structures are built once at startup and never touched again:
//
//   - a membership set of every ASCII-only word form ("laska", "pisze") —
//     used to skip words that are already correct, and
//   - a mapping from ASCII-folded forms to their diacritic candidates
//     ("zolw" → "żółw", "zle" → "złe", "źle").
//
// Both are stored as sorted string blobs with offset tables and queried by
// binary search: millions of forms fit in tens of megabytes, lookups are
// sub-microsecond and allocation-free, and the structures are immutable, so
// they are safe for concurrent readers.
package dict

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/andresousadotpt/texpand/internal/fold"
	"github.com/andresousadotpt/texpand/internal/hunspell"
)

// stringTable is a sorted sequence of strings packed into one blob.
type stringTable struct {
	blob string
	off  []uint32 // len(off) == count+1
}

func (t *stringTable) count() int { return len(t.off) - 1 }

func (t *stringTable) at(i int) string { return t.blob[t.off[i]:t.off[i+1]] }

// search returns the first index whose string is >= s.
func (t *stringTable) search(s string) int {
	return sort.Search(t.count(), func(i int) bool { return t.at(i) >= s })
}

func (t *stringTable) contains(s string) bool {
	i := t.search(s)
	return i < t.count() && t.at(i) == s
}

// Index is the immutable word index.
type Index struct {
	ascii    stringTable // ASCII-only word forms, sorted, unique
	candKeys stringTable // folded keys, sorted; parallel to candVals
	candVals stringTable // diacritic forms; pairs sorted by (key, val), unique
}

// IsWord reports whether the lowercase ASCII word is a known word form.
func (ix *Index) IsWord(lower string) bool {
	return ix.ascii.contains(lower)
}

// Candidates appends the distinct diacritic candidates for the folded
// lowercase key to buf and returns it. It allocates nothing when buf has
// capacity.
func (ix *Index) Candidates(folded string, buf []string) []string {
	i := ix.candKeys.search(folded)
	for ; i < ix.candKeys.count() && ix.candKeys.at(i) == folded; i++ {
		v := ix.candVals.at(i)
		// Pairs are sorted, so duplicates are adjacent.
		if n := len(buf); n > 0 && buf[n-1] == v {
			continue
		}
		buf = append(buf, v)
	}
	return buf
}

// Stats returns the number of ASCII word forms and candidate pairs.
func (ix *Index) Stats() (asciiWords, candidatePairs int) {
	return ix.ascii.count(), ix.candKeys.count()
}

// Locate resolves the configured dictionary setting to .dic and .aff paths.
// "auto" (or "") probes standard Hunspell locations for a Polish dictionary.
func Locate(configured string) (dicPath, affPath string, err error) {
	if configured == "" || configured == "auto" {
		for _, p := range []string{
			"/usr/share/hunspell/pl_PL.dic",
			"/usr/share/myspell/dicts/pl_PL.dic",
			"/usr/share/myspell/pl_PL.dic",
		} {
			if _, err := os.Stat(p); err == nil {
				return p, affFor(p), nil
			}
		}
		return "", "", fmt.Errorf("no Polish Hunspell dictionary found (install hunspell-pl or set autocorrect.dictionary)")
	}
	if _, err := os.Stat(configured); err != nil {
		return "", "", fmt.Errorf("configured dictionary %s: %w", configured, err)
	}
	return configured, affFor(configured), nil
}

func affFor(dic string) string {
	return strings.TrimSuffix(dic, filepath.Ext(dic)) + ".aff"
}

// builder accumulates word forms into packed buffers before sorting.
type builder struct {
	buf []byte
	off []uint32
}

func (b *builder) add(s string) {
	b.buf = append(b.buf, s...)
	b.off = append(b.off, uint32(len(b.buf)))
}

func (b *builder) count() int { return len(b.off) }

// at returns entry i as a zero-copy view into the packed buffer.
func (b *builder) at(i int) []byte {
	start := uint32(0)
	if i > 0 {
		start = b.off[i-1]
	}
	return b.buf[start:b.off[i]]
}

// finishSorted sorts entry indices with less, skips entries equal to their
// predecessor, and passes the survivors to emit in order.
func (b *builder) finishSorted(less func(i, j int) bool, equal func(i, j int) bool, emit func(i int)) {
	idx := make([]int, b.count())
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(x, y int) bool { return less(idx[x], idx[y]) })
	for n, i := range idx {
		if n > 0 && equal(idx[n-1], i) {
			continue
		}
		emit(i)
	}
}

// Build parses the dictionary pair and constructs the index. Malformed
// lines are skipped; only a completely unreadable file is an error.
func Build(dicPath, affPath string) (*Index, error) {
	affFile, err := os.Open(affPath)
	if err != nil {
		return nil, fmt.Errorf("open .aff: %w", err)
	}
	aff, err := hunspell.ParseAff(affFile)
	affFile.Close()
	if err != nil {
		return nil, err
	}

	dicFile, err := os.Open(dicPath)
	if err != nil {
		return nil, fmt.Errorf("open .dic: %w", err)
	}
	defer dicFile.Close()

	var ascii, keys, vals builder

	emit := func(form string) {
		if hasUpper(form) {
			form = strings.ToLower(form)
		}
		if fold.IsASCIILetters(form) {
			ascii.add(form)
			return
		}
		if fold.IsPolishLetters(form) && fold.HasDiacritics(form) {
			keys.add(fold.Fold(form))
			vals.add(form)
		}
		// Forms with digits or non-Polish characters are irrelevant to
		// diacritics correction and are dropped.
	}

	sc := bufio.NewScanner(dicFile)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		entry, ok := hunspell.ParseDicLine(sc.Text(), aff.FlagMode())
		if !ok {
			continue
		}
		aff.Expand(entry, emit)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read .dic: %w", err)
	}

	ix := &Index{}

	// ASCII membership set: sort + dedupe.
	{
		var blob []byte
		off := []uint32{0}
		ascii.finishSorted(
			func(i, j int) bool { return bytes.Compare(ascii.at(i), ascii.at(j)) < 0 },
			func(i, j int) bool { return bytes.Equal(ascii.at(i), ascii.at(j)) },
			func(i int) {
				blob = append(blob, ascii.at(i)...)
				off = append(off, uint32(len(blob)))
			},
		)
		ix.ascii = stringTable{blob: string(blob), off: off}
	}

	// Candidate pairs: sort by (key, val) + dedupe.
	{
		var kblob, vblob []byte
		koff, voff := []uint32{0}, []uint32{0}
		keys.finishSorted(
			func(i, j int) bool {
				if c := bytes.Compare(keys.at(i), keys.at(j)); c != 0 {
					return c < 0
				}
				return bytes.Compare(vals.at(i), vals.at(j)) < 0
			},
			func(i, j int) bool {
				return bytes.Equal(keys.at(i), keys.at(j)) && bytes.Equal(vals.at(i), vals.at(j))
			},
			func(i int) {
				kblob = append(kblob, keys.at(i)...)
				koff = append(koff, uint32(len(kblob)))
				vblob = append(vblob, vals.at(i)...)
				voff = append(voff, uint32(len(vblob)))
			},
		)
		ix.candKeys = stringTable{blob: string(kblob), off: koff}
		ix.candVals = stringTable{blob: string(vblob), off: voff}
	}

	return ix, nil
}

func hasUpper(s string) bool {
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			return true
		}
		switch r {
		case 'Ą', 'Ć', 'Ę', 'Ł', 'Ń', 'Ó', 'Ś', 'Ź', 'Ż':
			return true
		}
	}
	return false
}
