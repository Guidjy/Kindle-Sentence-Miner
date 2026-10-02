package importer

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/xythh/ann2html/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func writeZip(t *testing.T, files map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dict.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(content))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

func count(t *testing.T, st *store.Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := st.DB.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestImportZip(t *testing.T) {
	st := openStore(t)
	p := writeZip(t, map[string]string{
		"index.json":            `{"title":"TestDict","revision":"1","format":3,"sequenced":true}`,
		"term_bank_1.json":      `[["食べる","たべる","v1","v1",10,["to eat"],1,""],["飲む","のむ","v5","v5",5,[{"type":"structured-content","content":"to drink"}],2,"P"]]`,
		"term_meta_bank_1.json": `[["食べる","freq",{"reading":"たべる","frequency":500}]]`,
		"tag_bank_1.json":       `[["v1","partOfSpeech",0,"Ichidan verb",0]]`,
		"styles.css":            `.x{color:red}`,
		"img/a.png":             "PNG",
	})
	if err := Import(st, p, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM terms`); n != 2 {
		t.Errorf("terms = %d, want 2", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM term_meta WHERE mode='freq'`); n != 1 {
		t.Errorf("term_meta = %d, want 1", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM media WHERE path='img/a.png'`); n != 1 {
		t.Errorf("media = %d, want 1", n)
	}
	var styles, gloss string
	st.DB.QueryRow(`SELECT styles FROM dictionaries WHERE title='TestDict'`).Scan(&styles)
	if styles != ".x{color:red}" {
		t.Errorf("styles = %q", styles)
	}
	st.DB.QueryRow(`SELECT glossary FROM terms WHERE expression='食べる'`).Scan(&gloss)
	if gloss != `["to eat"]` {
		t.Errorf("glossary = %q", gloss)
	}

	// Re-importing replaces instead of duplicating.
	if err := Import(st, p, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM terms`); n != 2 {
		t.Errorf("after re-import terms = %d, want 2", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM dictionaries`); n != 1 {
		t.Errorf("dictionaries = %d, want 1", n)
	}
}

func TestImportCollection(t *testing.T) {
	st := openStore(t)
	p := filepath.Join(t.TempDir(), "yomitan-dictionaries.json")
	os.WriteFile(p, []byte(`{"formatName":"dexie","formatVersion":1,"data":{"databaseName":"dict","databaseVersion":60,
	"tables":[{"name":"terms","schema":"++id","rowCount":1}],
	"data":[
	 {"tableName":"terms","inbound":true,"rows":[
	  {"expression":"猫","reading":"ねこ","definitionTags":"n","rules":"","score":0,"glossary":["cat"],"sequence":7,"termTags":"","dictionary":"D1","id":1}
	 ]},
	 {"tableName":"kanji","inbound":false,"rows":[{"$":[1,{"character":"猫","dictionary":"D1"}],"$types":{"$":{"":"arrayNonindexKeys"}}}]},
	 {"tableName":"dictionaries","inbound":false,"rows":[{"$":[1,{"title":"D1","revision":"r","version":3,"sequenced":true,"styles":"s"}],"$types":{"$":{"":"arrayNonindexKeys"}}}]},
	 {"tableName":"termMeta","inbound":false,"rows":[{"$":[1,{"expression":"猫","mode":"pitch","data":{"reading":"ねこ","pitches":[{"position":1}]},"dictionary":"D1"}],"$types":{"$":{"":"arrayNonindexKeys","1.data.pitches":"arrayNonindexKeys"}}},{"$":[2,{"expression":"猫","mode":"freq","data":{"reading":"ねこ","frequency":{"value":500,"displayValue":"500"}},"dictionary":"D1"}],"$types":{"$":{"":"arrayNonindexKeys"}}}]},
	 {"tableName":"tagMeta","inbound":false,"rows":[{"$":[1,{"name":"n","category":"partOfSpeech","order":0,"notes":"noun","score":0,"dictionary":"D1"}],"$types":{"$":{"":"arrayNonindexKeys"}}}]},
	 {"tableName":"media","inbound":true,"rows":[{"dictionary":"D1","path":"a.png","mediaType":"image/png","width":1,"height":1,"content":"UE5H","$types":{"content":"arraybuffer"}}]}
	]}}`), 0o644)
	if err := Import(st, p, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM terms WHERE expression='猫' AND sequence=7`); n != 1 {
		t.Errorf("terms = %d", n)
	}
	var rev, styles string
	st.DB.QueryRow(`SELECT revision, styles FROM dictionaries WHERE title='D1'`).Scan(&rev, &styles)
	if rev != "r" || styles != "s" {
		t.Errorf("dictionary row = %q %q", rev, styles)
	}
	var content []byte
	st.DB.QueryRow(`SELECT content FROM media WHERE path='a.png'`).Scan(&content)
	if string(content) != "PNG" {
		t.Errorf("media content = %q", content)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tag_meta`); n != 1 {
		t.Errorf("tags = %d", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM term_meta m JOIN dictionaries d ON d.id = m.dict_id WHERE d.title = 'D1' AND m.mode IN ('pitch', 'freq')`); n != 2 {
		t.Errorf("term meta = %d, want 2", n)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM tag_meta WHERE name = 'n' AND category = 'partOfSpeech'`); n != 1 {
		t.Errorf("tag not imported")
	}

	// A collection import replaces dictionaries imported before.
	if err := Import(st, p, nil); err != nil {
		t.Fatal(err)
	}
	if n := count(t, st, `SELECT COUNT(*) FROM dictionaries`); n != 1 {
		t.Errorf("dictionaries after re-import = %d", n)
	}
}
