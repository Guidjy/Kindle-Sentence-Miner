package miner

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/importer"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

// TestGoldenTemae mines 手間 from a real sentence with real dictionary data
// and compares the note with one Yomitan created for the same sentence.
func TestGoldenTemae(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	dicts, _ := filepath.Glob("testdata/temae/dict*")
	for _, d := range dicts {
		zp := filepath.Join(dir, filepath.Base(d)+".zip")
		zf, _ := os.Create(zp)
		zw := zip.NewWriter(zf)
		files, _ := os.ReadDir(d)
		for _, f := range files {
			data, _ := os.ReadFile(filepath.Join(d, f.Name()))
			w, _ := zw.Create(f.Name())
			w.Write(data)
		}
		zw.Close()
		zf.Close()
		if err := importer.Import(st, zp, nil); err != nil {
			t.Fatal(err)
		}
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "vocab.db"))
	_, err = db.Exec(`
	CREATE TABLE WORDS (id TEXT PRIMARY KEY, word TEXT, stem TEXT, lang TEXT, category INTEGER, timestamp INTEGER, profileid TEXT);
	CREATE TABLE LOOKUPS (id TEXT PRIMARY KEY, word_key TEXT, book_key TEXT, dict_key TEXT, pos TEXT, usage TEXT, timestamp INTEGER);
	CREATE TABLE BOOK_INFO (id TEXT PRIMARY KEY, asin TEXT, guid TEXT, lang TEXT, title TEXT, authors TEXT);
	INSERT INTO WORDS VALUES ('ja:手間','手間','手間','ja',0,0,'');
	INSERT INTO LOOKUPS VALUES ('1','ja:手間','b','','','「探せばいるかもしれないけど、少し手間ね」',10);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAnki{media: map[string]int{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	export, err := settings.Parse([]byte(`{"options":{"profileCurrent":0,"profiles":[{"name":"Default","options":{
	 "general":{"resultOutputMode":"group","glossaryLayoutMode":"default","compactTags":false,"mainDictionary":"Jitendex.org [2025-10-01]","language":"ja"},
	 "dictionaries":[{"name":"Jitendex.org [2025-10-01]","alias":"Jitendex.org [2025-10-01]","enabled":true},{"name":"JMnedict [2025-10-10]","alias":"JMnedict [2025-10-10]","enabled":true},
	  {"name":"KireiCake","alias":"KireiCake","enabled":true},{"name":"Kanjium Pitch Accents","alias":"Kanjium Pitch Accents","enabled":true},
	  {"name":"BCCWJ","alias":"BCCWJ","enabled":true},{"name":"JPDBv2㋕","alias":"JPDBv2㋕","enabled":true},{"name":"KANJIDIC [2025-283]","alias":"KANJIDIC [2025-283]","enabled":true}],
	 "anki":{"server":"` + srv.URL + `","tags":[],"duplicateScope":"collection","checkForDuplicates":true,"fieldTemplates":null,
	  "cardFormats":[{"type":"term","deck":"Mining","model":"Lapis","fields":{
	   "Expression":{"value":"{expression}"},"ExpressionFurigana":{"value":"{furigana-plain}"},"ExpressionReading":{"value":"{reading}"},
	   "MainDefinition":{"value":"{single-glossary-jitendexorg-2025-10-01}"},"Sentence":{"value":"{cloze-prefix}<b>{cloze-body}</b>{cloze-suffix}"},
	   "Glossary":{"value":"{glossary}"},"PitchPosition":{"value":"{pitch-accent-positions}"},"PitchCategories":{"value":"{pitch-accent-categories}"},
	   "Frequency":{"value":"{frequencies}"},"FreqSort":{"value":"{frequency-harmonic-rank}"}}}]},
	 "audio":{"sources":[]}}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	status, err := Run(context.Background(), Config{Store: st, VocabPath: filepath.Join(dir, "vocab.db"), Settings: export}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Added != 1 {
		t.Fatalf("status = %+v", status)
	}

	var want map[string]string
	data, _ := os.ReadFile("testdata/temae/expected.json")
	json.Unmarshal(data, &want)
	got := fake.notes[0].Fields
	for field, w := range want {
		if g := got[field]; g != w {
			t.Errorf("%s differs at byte %d:\n got  %s\n want %s", field, firstDiff(g, w), excerpt(g, firstDiff(g, w)), excerpt(w, firstDiff(g, w)))
		}
	}
}

func firstDiff(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

func excerpt(s string, at int) string {
	start := max(0, at-120)
	end := min(len(s), at+200)
	return strings.ToValidUTF8(s[start:end], "?")
}
