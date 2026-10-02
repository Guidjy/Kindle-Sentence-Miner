package miner

import (
	"archive/zip"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xythh/ann2html/internal/anki"
	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/importer"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

// fakeAnki is an in-memory AnkiConnect that treats notes with the same first
// field value as duplicates.
type fakeAnki struct {
	mu     sync.Mutex
	notes  []anki.Note
	media  map[string]int
	failOn string
}

func (f *fakeAnki) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
		Params struct {
			Notes    []anki.Note `json:"notes"`
			Note     anki.Note   `json:"note"`
			Filename string      `json:"filename"`
		} `json:"params"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(result any, err any) {
		json.NewEncoder(w).Encode(map[string]any{"result": result, "error": err})
	}
	exists := func(n anki.Note) bool {
		for _, existing := range f.notes {
			if existing.Fields["Expression"] == n.Fields["Expression"] {
				return true
			}
		}
		return false
	}
	switch req.Action {
	case "version":
		reply(6, nil)
	case "modelFieldNames":
		reply([]string{"Expression", "Sentence", "Glossary", "Audio"}, nil)
	case "canAddNotesWithErrorDetail":
		var out []map[string]any
		for _, n := range req.Params.Notes {
			if exists(n) {
				out = append(out, map[string]any{"canAdd": false, "error": "cannot create note because it is a duplicate"})
			} else {
				out = append(out, map[string]any{"canAdd": true})
			}
		}
		reply(out, nil)
	case "addNote":
		if f.failOn != "" && req.Params.Note.Fields["Expression"] == f.failOn {
			reply(nil, "collection is not available")
			return
		}
		if exists(req.Params.Note) {
			reply(nil, "cannot create note because it is a duplicate")
			return
		}
		f.notes = append(f.notes, req.Params.Note)
		reply(len(f.notes), nil)
	case "storeMediaFile":
		f.media[req.Params.Filename]++
		reply(req.Params.Filename, nil)
	default:
		reply(nil, "unsupported action")
	}
}

func fixture(t *testing.T) (dir string, st *store.Store) {
	t.Helper()
	dir = t.TempDir()
	st, err := store.Open(filepath.Join(dir, "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	zp := filepath.Join(dir, "d.zip")
	zf, _ := os.Create(zp)
	zw := zip.NewWriter(zf)
	for name, content := range map[string]string{
		"index.json":       `{"title":"JMdict","revision":"1","format":3}`,
		"term_bank_1.json": `[["食べる","たべる","v1","v1",0,["to eat"],1,""],["猫","ねこ","n","",0,[{"type":"image","path":"cat.png","width":2,"height":2}],2,""],["本","ほん","n","",0,["book"],3,""]]`,
		"cat.png":          "PNG",
	} {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	zf.Close()
	if err := importer.Import(st, zp, nil); err != nil {
		t.Fatal(err)
	}

	db, _ := sql.Open("sqlite", filepath.Join(dir, "vocab.db"))
	_, err = db.Exec(`
	CREATE TABLE WORDS (id TEXT PRIMARY KEY, word TEXT, stem TEXT, lang TEXT, category INTEGER, timestamp INTEGER, profileid TEXT);
	CREATE TABLE LOOKUPS (id TEXT PRIMARY KEY, word_key TEXT, book_key TEXT, dict_key TEXT, pos TEXT, usage TEXT, timestamp INTEGER);
	CREATE TABLE BOOK_INFO (id TEXT PRIMARY KEY, asin TEXT, guid TEXT, lang TEXT, title TEXT, authors TEXT);
	INSERT INTO WORDS VALUES ('ja:食べる','食べた','食べる','ja',0,0,''), ('ja:猫','猫','猫','ja',0,0,''),
	                         ('ja:本','本','本','ja',0,0,''), ('ja:無','無','無い語','ja',0,0,'');
	INSERT INTO BOOK_INFO VALUES ('b','','','ja','Book','');
	INSERT INTO LOOKUPS VALUES ('1','ja:食べる','b','','','寿司を食べた。',10), ('2','ja:食べる','b','','','また食べた。',20),
	                           ('3','ja:猫','b','','','猫がいる。',30), ('4','ja:無','b','','','無',40), ('5','ja:本','b','','','本を読む。',50);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	return dir, st
}

func exportFor(url string) *settings.Export {
	e, err := settings.Parse([]byte(`{"options":{"profileCurrent":0,"profiles":[{"name":"Default","options":{
	 "general":{"resultOutputMode":"group"},
	 "dictionaries":[{"name":"JMdict","alias":"","enabled":true}],
	 "anki":{"server":"` + url + `","tags":["kindle"],"duplicateScope":"collection","checkForDuplicates":true,
	  "cardFormats":[{"type":"term","deck":"Mining","model":"Lapis","fields":{
	   "Expression":{"value":"{expression}"},"Sentence":{"value":"{cloze-prefix}<b>{cloze-body}</b>{cloze-suffix}"},
	   "Glossary":{"value":"{glossary}"},"Audio":{"value":"{audio}"}}}]},
	 "audio":{"sources":[{"type":"custom","url":"` + url + `/audio/{term}"}]}}}]}}`))
	if err != nil {
		panic(err)
	}
	return e
}

func TestRun(t *testing.T) {
	dir, st := fixture(t)
	fake := &fakeAnki{media: map[string]int{}, failOn: "本"}
	mux := http.NewServeMux()
	mux.HandleFunc("/audio/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "猫") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Write([]byte("MP3" + r.URL.Path))
	})
	mux.Handle("/", fake)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := Config{Store: st, VocabPath: filepath.Join(dir, "vocab.db"), Languages: []string{"ja"}, Settings: exportFor(srv.URL)}
	status, err := Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 5 || status.Added != 2 || status.Duplicates != 1 || status.NoDefinition != 1 || status.Failed != 1 {
		t.Fatalf("status = %+v", status)
	}
	var eat anki.Note
	for _, n := range fake.notes {
		if n.Fields["Expression"] == "食べる" {
			eat = n
		}
	}
	if eat.Fields["Sentence"] != "寿司を<b>食べた</b>。" {
		t.Errorf("sentence = %q", eat.Fields["Sentence"])
	}
	if !strings.HasPrefix(eat.Fields["Audio"], "[sound:yomitan_audio_") || eat.DeckName != "Mining" || eat.Tags[0] != "kindle" {
		t.Errorf("note = %+v", eat)
	}
	if !strings.Contains(eat.Fields["Glossary"], "to eat") {
		t.Errorf("glossary = %q", eat.Fields["Glossary"])
	}
	if len(fake.media) != 3 { // audio for 食べる and 本 (uploaded before its add failed), the cat image
		t.Errorf("media = %v", fake.media)
	}
	// The failure at timestamp 50 must be retried next time.
	if ts, _ := st.LastTimestamp(); ts != 49 {
		t.Errorf("last timestamp = %d, want 49", ts)
	}

	// Second run: Anki works again; only the failed note is new.
	fake.failOn = ""
	status, err = Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Total != 1 || status.Added != 1 {
		t.Fatalf("second run status = %+v", status)
	}
	if ts, _ := st.LastTimestamp(); ts != 50 {
		t.Errorf("last timestamp = %d, want 50", ts)
	}

	// Rescan: everything is already in Anki.
	cfg.RescanAll = true
	status, err = Run(context.Background(), cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.Added != 0 || status.Duplicates != 4 {
		t.Fatalf("rescan status = %+v", status)
	}
	if len(fake.notes) != 3 {
		t.Errorf("notes in anki = %d", len(fake.notes))
	}
}
