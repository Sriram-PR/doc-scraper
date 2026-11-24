package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
)

var pageMarker = []byte(`"record_type":"page"`)

// pageStore finds page records in stored crawls, rescanning a pages.jsonl only
// when it changed since the last read. The zero value is ready to use.
type pageStore struct {
	mu       sync.Mutex
	catalogs map[string]*catalog
}

// catalog indexes one pages.jsonl as of the file described by info.
type catalog struct {
	mu      sync.Mutex
	info    os.FileInfo
	entries []catalogEntry
	exact   map[string]int
	norm    map[string]int
}

type catalogEntry struct {
	offset int64
	length int
	url    string
}

type catalogRecord struct {
	RecordType string `json:"record_type"`
	URL        string `json:"url"`
}

// Page returns the first page record whose URL equals pageURL or has the same
// normalized form; nil, nil when the file has none.
func (ps *pageStore) Page(jsonlPath, pageURL string) (*models.PageJSONL, error) {
	f, c, err := ps.open(jsonlPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	defer c.mu.Unlock()

	i := c.find(pageURL)
	if i < 0 {
		return nil, nil
	}
	if p, err := c.read(f, i); err == nil {
		return p, nil
	}
	// Rewritten in place without a size or mod time change.
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := c.build(f, info); err != nil {
		return nil, err
	}
	if i = c.find(pageURL); i < 0 {
		return nil, nil
	}
	return c.read(f, i)
}

// open returns the opened file and its catalog, current for that file and
// locked; the caller unlocks the catalog and closes the file.
func (ps *pageStore) open(path string) (*os.File, *catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	ps.mu.Lock()
	if ps.catalogs == nil {
		ps.catalogs = make(map[string]*catalog)
	}
	c := ps.catalogs[path]
	if c == nil {
		c = &catalog{}
		ps.catalogs[path] = c
	}
	ps.mu.Unlock()

	c.mu.Lock()
	if !c.current(info) {
		if err := c.build(f, info); err != nil {
			c.mu.Unlock()
			f.Close()
			return nil, nil, err
		}
	}
	return f, c, nil
}

func (c *catalog) current(info os.FileInfo) bool {
	return c.info != nil && os.SameFile(c.info, info) &&
		c.info.Size() == info.Size() && c.info.ModTime().Equal(info.ModTime())
}

// build indexes the first info.Size() bytes of f, so a concurrent append
// can't make the catalog describe more than info does.
func (c *catalog) build(f *os.File, info os.FileInfo) error {
	c.info = nil
	var entries []catalogEntry
	exact := make(map[string]int)
	norm := make(map[string]int)

	r := bufio.NewReaderSize(io.NewSectionReader(f, 0, info.Size()), 64*1024)
	var offset int64
	var line []byte
	for {
		line = line[:0]
		var err error
		for {
			var chunk []byte
			chunk, err = r.ReadSlice('\n')
			line = append(line, chunk...)
			if !errors.Is(err, bufio.ErrBufferFull) {
				break
			}
		}
		atEOF := errors.Is(err, io.EOF)
		if err != nil && !atEOF {
			return err
		}
		start := offset
		offset += int64(len(line))

		if rec := bytes.TrimSpace(line); bytes.Contains(rec, pageMarker) {
			var p catalogRecord
			if json.Unmarshal(rec, &p) == nil && p.RecordType == models.RecordTypePage {
				i := len(entries)
				entries = append(entries, catalogEntry{offset: start, length: len(line), url: p.URL})
				if _, ok := exact[p.URL]; !ok {
					exact[p.URL] = i
				}
				if key, _, _, err := parse.ParseURL(p.URL); err == nil {
					if _, ok := norm[key]; !ok {
						norm[key] = i
					}
				}
			}
		}
		if atEOF {
			break
		}
	}
	c.entries, c.exact, c.norm, c.info = entries, exact, norm, info
	return nil
}

// find returns the index of the first entry matching pageURL exactly or by
// normalized form, or -1.
func (c *catalog) find(pageURL string) int {
	i, ok := c.exact[pageURL]
	if !ok {
		i = -1
	}
	if key, _, _, err := parse.ParseURL(pageURL); err == nil {
		if j, ok := c.norm[key]; ok && (i < 0 || j < i) {
			i = j
		}
	}
	return i
}

func (c *catalog) read(f *os.File, i int) (*models.PageJSONL, error) {
	e := c.entries[i]
	buf := make([]byte, e.length)
	if _, err := f.ReadAt(buf, e.offset); err != nil {
		return nil, err
	}
	var p models.PageJSONL
	if err := json.Unmarshal(bytes.TrimSpace(buf), &p); err != nil {
		return nil, err
	}
	if p.RecordType != models.RecordTypePage || p.URL != e.url {
		return nil, fmt.Errorf("record at offset %d is no longer %s", e.offset, e.url)
	}
	return &p, nil
}
