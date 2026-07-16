package dict

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// cacheMagic identifies the cache file format; bump the version on any
// layout change.
const cacheMagic = "TXPDICT2"

// cacheHeader ties a cache file to the exact dictionary files it was built
// from. Any mismatch invalidates the cache.
type cacheHeader struct {
	Dic      string `json:"dic"`
	Aff      string `json:"aff"`
	DicSize  int64  `json:"dic_size"`
	DicMtime int64  `json:"dic_mtime"`
	AffSize  int64  `json:"aff_size"`
	AffMtime int64  `json:"aff_mtime"`
}

func headerFor(dicPath, affPath string) (cacheHeader, error) {
	di, err := os.Stat(dicPath)
	if err != nil {
		return cacheHeader{}, err
	}
	ai, err := os.Stat(affPath)
	if err != nil {
		return cacheHeader{}, err
	}
	return cacheHeader{
		Dic: dicPath, Aff: affPath,
		DicSize: di.Size(), DicMtime: di.ModTime().UnixNano(),
		AffSize: ai.Size(), AffMtime: ai.ModTime().UnixNano(),
	}, nil
}

// CachePath returns the default cache file location.
func CachePath() string {
	if d := os.Getenv("XDG_CACHE_HOME"); d != "" {
		return filepath.Join(d, "texpand", "pl-index.cache")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "texpand", "pl-index.cache")
}

// SaveCache writes the index to path atomically (temp file + rename).
func SaveCache(path string, ix *Index, dicPath, affPath string) error {
	hdr, err := headerFor(dicPath, affPath)
	if err != nil {
		return err
	}
	hdrJSON, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".pl-index-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriterSize(tmp, 1<<20)

	write := func(b []byte) error {
		if err := binary.Write(w, binary.LittleEndian, uint64(len(b))); err != nil {
			return err
		}
		_, err := w.Write(b)
		return err
	}
	writeOff := func(off []uint32) error {
		b := make([]byte, 8+4*len(off))
		binary.LittleEndian.PutUint64(b, uint64(len(off)))
		for i, v := range off {
			binary.LittleEndian.PutUint32(b[8+4*i:], v)
		}
		_, err := w.Write(b)
		return err
	}

	if _, err := w.WriteString(cacheMagic); err != nil {
		return err
	}
	if err := write(hdrJSON); err != nil {
		return err
	}
	for _, t := range []*stringTable{&ix.ascii, &ix.candKeys, &ix.candVals} {
		if err := write([]byte(t.blob)); err != nil {
			return err
		}
		if err := writeOff(t.off); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// LoadCache reads the index from path if the cache matches the given
// dictionary files exactly; otherwise it returns an error and the caller
// rebuilds.
func LoadCache(path, dicPath, affPath string) (*Index, error) {
	want, err := headerFor(dicPath, affPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pos := 0
	if len(data) < len(cacheMagic) || string(data[:len(cacheMagic)]) != cacheMagic {
		return nil, fmt.Errorf("cache: bad magic")
	}
	pos += len(cacheMagic)

	readBytes := func() ([]byte, error) {
		if pos+8 > len(data) {
			return nil, fmt.Errorf("cache: truncated")
		}
		n := int(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		if n < 0 || pos+n > len(data) {
			return nil, fmt.Errorf("cache: truncated")
		}
		b := data[pos : pos+n]
		pos += n
		return b, nil
	}
	readOff := func() ([]uint32, error) {
		if pos+8 > len(data) {
			return nil, fmt.Errorf("cache: truncated")
		}
		n := int(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		if n < 1 || pos+4*n > len(data) {
			return nil, fmt.Errorf("cache: truncated")
		}
		off := make([]uint32, n)
		for i := range off {
			off[i] = binary.LittleEndian.Uint32(data[pos+4*i:])
		}
		pos += 4 * n
		return off, nil
	}

	hdrJSON, err := readBytes()
	if err != nil {
		return nil, err
	}
	var got cacheHeader
	if err := json.Unmarshal(hdrJSON, &got); err != nil {
		return nil, fmt.Errorf("cache: bad header: %w", err)
	}
	if got != want {
		return nil, fmt.Errorf("cache: stale (dictionary changed)")
	}

	ix := &Index{}
	for _, t := range []*stringTable{&ix.ascii, &ix.candKeys, &ix.candVals} {
		blob, err := readBytes()
		if err != nil {
			return nil, err
		}
		off, err := readOff()
		if err != nil {
			return nil, err
		}
		if off[0] != 0 || int(off[len(off)-1]) != len(blob) {
			return nil, fmt.Errorf("cache: corrupt offsets")
		}
		for i := 1; i < len(off); i++ {
			if off[i] < off[i-1] {
				return nil, fmt.Errorf("cache: corrupt offsets")
			}
		}
		t.blob = string(blob)
		t.off = off
	}
	if ix.candKeys.count() != ix.candVals.count() {
		return nil, fmt.Errorf("cache: corrupt (key/value count mismatch)")
	}
	return ix, nil
}
