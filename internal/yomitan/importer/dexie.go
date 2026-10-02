package importer

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/xythh/ann2html/internal/store"
)

// countingReader tracks how many bytes have been read for progress reporting.
type countingReader struct {
	r io.Reader
	n atomic.Int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// ImportCollection streams a Yomitan "Export dictionary collection" file
// (a dexie-export-import JSON dump of Yomitan's "dict" IndexedDB database)
// into the store. The file can be several gigabytes, so it is never fully
// loaded into memory.
func ImportCollection(st *store.Store, path string, progress Progress) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	cr := &countingReader{r: bufio.NewReaderSize(f, 1<<20)}
	dec := json.NewDecoder(cr)
	dec.UseNumber()

	w, err := newWriter(st)
	if err != nil {
		return err
	}
	defer func() { err = w.finish(err) }()

	report := func(table string) {
		if progress != nil {
			progress("Importing "+table, cr.n.Load(), info.Size())
		}
	}

	// Top level: {"formatName": ..., "formatVersion": ..., "data": {...}}
	if err := expectDelim(dec, '{'); err != nil {
		return fmt.Errorf("not a dictionary collection export: %w", err)
	}
	foundData := false
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		if key != "data" {
			if err := skipValue(dec); err != nil {
				return err
			}
			continue
		}
		foundData = true
		if err := importDataObject(dec, w, report); err != nil {
			return err
		}
	}
	if !foundData {
		return fmt.Errorf("not a dictionary collection export: no \"data\" section")
	}
	report("done")
	return nil
}

// importDataObject reads {"databaseName":..., "tables":[...], "data":[{tableName, rows}...]}.
func importDataObject(dec *json.Decoder, w *writer, report func(string)) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "databaseName":
			var name string
			if err := dec.Decode(&name); err != nil {
				return err
			}
			if name != "dict" {
				return fmt.Errorf("unexpected database %q, expected Yomitan's \"dict\" database", name)
			}
		case "data":
			if err := expectDelim(dec, '['); err != nil {
				return err
			}
			for dec.More() {
				if err := importTable(dec, w, report); err != nil {
					return err
				}
			}
			if err := expectDelim(dec, ']'); err != nil {
				return err
			}
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	return expectDelim(dec, '}')
}

// importTable reads {"tableName": "...", "inbound": bool, "rows": [...]}.
func importTable(dec *json.Decoder, w *writer, report func(string)) error {
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	table := ""
	for dec.More() {
		key, err := readKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "tableName":
			if err := dec.Decode(&table); err != nil {
				return err
			}
		case "rows":
			if table == "" {
				return fmt.Errorf("table rows found before tableName")
			}
			if err := expectDelim(dec, '['); err != nil {
				return err
			}
			n := 0
			for dec.More() {
				if err := importRow(dec, w, table); err != nil {
					return fmt.Errorf("%s row %d: %w", table, n, err)
				}
				n++
				if n%5000 == 0 {
					report(table)
				}
			}
			if err := expectDelim(dec, ']'); err != nil {
				return err
			}
			report(table)
		default:
			if err := skipValue(dec); err != nil {
				return err
			}
		}
	}
	return expectDelim(dec, '}')
}

func importRow(dec *json.Decoder, w *writer, table string) error {
	switch table {
	case "dictionaries":
		var d dictSummary
		if err := dec.Decode(&d); err != nil {
			return err
		}
		return w.addDictionary(d)
	case "terms":
		var t termRow
		if err := dec.Decode(&t); err != nil {
			return err
		}
		return w.addTerm(t)
	case "termMeta":
		var m termMetaRow
		if err := dec.Decode(&m); err != nil {
			return err
		}
		return w.addTermMeta(m)
	case "tagMeta":
		var t tagRow
		if err := dec.Decode(&t); err != nil {
			return err
		}
		return w.addTag(t)
	case "media":
		var raw struct {
			mediaRow
			Content json.RawMessage `json:"content"`
		}
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		m := raw.mediaRow
		content, err := decodeBinary(raw.Content)
		if err != nil {
			return fmt.Errorf("media %s: %w", m.Path, err)
		}
		m.Content = content
		return w.addMedia(m)
	default:
		// kanji, kanjiMeta and anything unknown are not needed for term cards.
		return skipValue(dec)
	}
}

// decodeBinary decodes binary values as written by dexie-export-import:
// ArrayBuffers are base64 strings (tagged in "$types"), Blobs are
// {"type": mime, "data": base64}.
func decodeBinary(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return base64.StdEncoding.DecodeString(s)
	}
	var blob struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(blob.Data)
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, got %v", want, tok)
	}
	return nil
}

func readKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("expected object key, got %v", tok)
	}
	return key, nil
}

// skipValue consumes the next JSON value without keeping it in memory.
func skipValue(dec *json.Decoder) error {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
			}
		}
		if depth == 0 {
			return nil
		}
	}
}
