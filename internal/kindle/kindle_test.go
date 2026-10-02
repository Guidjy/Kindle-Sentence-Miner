package kindle

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReadLookups(t *testing.T) {
	p := filepath.Join(t.TempDir(), "vocab.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
	CREATE TABLE WORDS (id TEXT PRIMARY KEY NOT NULL UNIQUE, word TEXT, stem TEXT, lang TEXT, category INTEGER DEFAULT 0, timestamp INTEGER DEFAULT 0, profileid TEXT);
	CREATE TABLE LOOKUPS (id TEXT PRIMARY KEY NOT NULL, word_key TEXT, book_key TEXT, dict_key TEXT, pos TEXT, usage TEXT, timestamp INTEGER DEFAULT 0);
	CREATE TABLE BOOK_INFO (id TEXT PRIMARY KEY NOT NULL, asin TEXT, guid TEXT, lang TEXT, title TEXT, authors TEXT);
	INSERT INTO WORDS VALUES ('ja:食べる','食べる','食べた','ja',0,0,''), ('en:run','run','running','en',0,0,'');
	INSERT INTO BOOK_INFO VALUES ('b1','','','ja','本','');
	INSERT INTO LOOKUPS VALUES ('l1','ja:食べる','b1','','','昨日寿司を食べた。',100),
	                           ('l2','en:run','b1','','','I was running.',200),
	                           ('l3','ja:食べる','b1','','','もう食べた？',50);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	got, err := ReadLookups(p, 60, []string{"ja"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d lookups, want 1: %+v", len(got), got)
	}
	l := got[0]
	if l.Lemma != "食べる" || l.Surface != "食べた" || l.BookTitle != "本" || l.Timestamp != 100 {
		t.Errorf("unexpected lookup %+v", l)
	}

	all, err := ReadLookups(p, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 || all[0].ID != "l3" {
		t.Errorf("want 3 lookups ordered by timestamp, got %+v", all)
	}
}

func TestMaybeSelection(t *testing.T) {
	cases := []struct {
		lk   Lookup
		want bool
	}{
		{Lookup{Lemma: "は、潔く", Surface: "は", Usage: "悪いことをした時は、潔く謝ったほうがいい。"}, true},
		{Lookup{Lemma: "として雇う", Surface: "として", Usage: "護衛として雇うつもりだ。"}, true},
		{Lookup{Lemma: "散らす", Surface: "散らし", Usage: "エリスはパッと髪を散らした。"}, false},
		{Lookup{Lemma: "家名", Surface: "家名", Usage: "家名に泥が付く"}, false},
	}
	for _, c := range cases {
		if got := c.lk.MaybeSelection(); got != c.want {
			t.Errorf("%s/%s: got %v", c.lk.Lemma, c.lk.Surface, got)
		}
	}
}
