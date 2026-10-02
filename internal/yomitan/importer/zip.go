package importer

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"path"
	"strings"

	"github.com/xythh/ann2html/internal/store"
)

// ImportZip imports a single Yomitan dictionary archive (index.json,
// term_bank_*.json, term_meta_bank_*.json, tag_bank_*.json, styles.css and
// media files).
func ImportZip(st *store.Store, zipPath string, progress Progress) (err error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()

	var index dictSummary
	var indexFile *zip.File
	for _, f := range zr.File {
		if f.Name == "index.json" {
			indexFile = f
		}
	}
	if indexFile == nil {
		return fmt.Errorf("%s: no index.json, not a Yomitan dictionary", path.Base(zipPath))
	}
	if err := readJSON(indexFile, &index); err != nil {
		return fmt.Errorf("index.json: %w", err)
	}
	if index.Title == "" {
		return fmt.Errorf("index.json has no title")
	}
	version := index.Format
	if version == 0 {
		version = index.Version
	}

	w, err := newWriter(st)
	if err != nil {
		return err
	}
	defer func() { err = w.finish(err) }()
	if err := w.addDictionary(index); err != nil {
		return err
	}

	total := int64(len(zr.File))
	for i, f := range zr.File {
		if progress != nil {
			progress("Importing "+index.Title, int64(i), total)
		}
		name := f.Name
		base := path.Base(name)
		switch {
		case f.FileInfo().IsDir() || name == "index.json":
		case strings.HasPrefix(base, "term_bank_") && strings.HasSuffix(base, ".json"):
			err = eachArrayItem(f, func(item []json.RawMessage) error {
				return w.addTerm(termFromTuple(index.Title, version, item))
			})
		case strings.HasPrefix(base, "term_meta_bank_") && strings.HasSuffix(base, ".json"):
			err = eachArrayItem(f, func(item []json.RawMessage) error {
				if len(item) < 3 {
					return nil
				}
				return w.addTermMeta(termMetaRow{
					Dictionary: index.Title,
					Expression: str(item[0]),
					Mode:       str(item[1]),
					Data:       item[2],
				})
			})
		case strings.HasPrefix(base, "tag_bank_") && strings.HasSuffix(base, ".json"):
			err = eachArrayItem(f, func(item []json.RawMessage) error {
				if len(item) < 5 {
					return nil
				}
				return w.addTag(tagRow{
					Dictionary: index.Title,
					Name:       str(item[0]),
					Category:   str(item[1]),
					Order:      num(item[2]),
					Notes:      str(item[3]),
					Score:      num(item[4]),
				})
			})
		case strings.HasPrefix(base, "kanji_bank_") || strings.HasPrefix(base, "kanji_meta_bank_"):
			// Kanji data is not needed for term cards.
		case name == "styles.css":
			var b []byte
			if b, err = readAll(f); err == nil {
				err = w.setStyles(index.Title, string(b))
			}
		case strings.HasSuffix(base, ".json"):
		default:
			var b []byte
			if b, err = readAll(f); err == nil {
				err = w.addMedia(mediaRow{
					Dictionary: index.Title,
					Path:       name,
					MediaType:  mime.TypeByExtension(path.Ext(name)),
					Content:    b,
				})
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	if progress != nil {
		progress("Imported "+index.Title, total, total)
	}
	return nil
}

// termFromTuple converts a term bank entry. Version 3:
// [expression, reading, definitionTags, rules, score, glossary, sequence, termTags].
// Version 1: [expression, reading, definitionTags, rules, score, ...glossaryStrings].
func termFromTuple(dict string, version int, item []json.RawMessage) termRow {
	get := func(i int) json.RawMessage {
		if i < len(item) {
			return item[i]
		}
		return nil
	}
	defTags := str(get(2))
	t := termRow{
		Dictionary:     dict,
		Expression:     str(get(0)),
		Reading:        str(get(1)),
		DefinitionTags: &defTags,
		Rules:          str(get(3)),
		Score:          num(get(4)),
	}
	if version == 1 {
		var gloss []json.RawMessage
		if len(item) > 5 {
			gloss = item[5:]
		}
		t.Glossary, _ = json.Marshal(gloss)
		return t
	}
	t.Glossary = get(5)
	seq := num(get(6))
	t.Sequence = &seq
	t.TermTags = str(get(7))
	return t
}

func str(raw json.RawMessage) string {
	var s string
	json.Unmarshal(raw, &s)
	return s
}

func num(raw json.RawMessage) float64 {
	var n float64
	json.Unmarshal(raw, &n)
	return n
}

func readAll(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func readJSON(f *zip.File, v any) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return json.NewDecoder(rc).Decode(v)
}

// eachArrayItem streams the tuples of a bank file ([[...], [...], ...]).
func eachArrayItem(f *zip.File, fn func([]json.RawMessage) error) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	dec := json.NewDecoder(rc)
	if err := expectDelim(dec, '['); err != nil {
		return err
	}
	for dec.More() {
		var item []json.RawMessage
		if err := dec.Decode(&item); err != nil {
			return err
		}
		if err := fn(item); err != nil {
			return err
		}
	}
	return nil
}
