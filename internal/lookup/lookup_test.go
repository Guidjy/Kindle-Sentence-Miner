package lookup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xythh/ann2html/internal/deinflect"
	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/importer"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

func importDict(t *testing.T, st *store.Store, files map[string]string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "d.zip")
	f, _ := os.Create(p)
	zw := zip.NewWriter(f)
	for name, content := range files {
		w, _ := zw.Create(name)
		w.Write([]byte(content))
	}
	zw.Close()
	f.Close()
	if err := importer.Import(st, p, nil); err != nil {
		t.Fatal(err)
	}
}

func setup(t *testing.T) *store.Store {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	importDict(t, st, map[string]string{
		"index.json":       `{"title":"A","revision":"1","format":3}`,
		"term_bank_1.json": `[["食べる","たべる","v1","v1",0,["to eat (A)"],1,""],["食べる","たべる","v1","v1",-1,["to consume (A)"],1,""]]`,
		"tag_bank_1.json":  `[["v1","partOfSpeech",-3,"Ichidan verb",0]]`,
	})
	importDict(t, st, map[string]string{
		"index.json":            `{"title":"B","revision":"1","format":3,"frequencyMode":"rank-based"}`,
		"term_bank_1.json":      `[["食べる","たべる","","v1",0,["eat (B)"],0,""],["喰べる","たべる","","v1",0,["eat, kanji variant (B)"],0,""]]`,
		"term_meta_bank_1.json": `[["食べる","freq",{"reading":"たべる","frequency":{"value":500,"displayValue":"500㋕"}}],["食べる","freq",{"reading":"たべた","frequency":1}],["食べる","pitch",{"reading":"たべる","pitches":[{"position":2}]}]]`,
	})
	return st
}

func TestFindDictionaryOrder(t *testing.T) {
	st := setup(t)
	for _, order := range [][]string{{"A", "B"}, {"B", "A"}} {
		var dicts []settings.Dictionary
		for _, n := range order {
			dicts = append(dicts, settings.Dictionary{Name: n, Enabled: true})
		}
		l, err := New(st, Options{Dictionaries: dicts, ResultOutputMode: "group"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := l.Find("食べる", "食べた")
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Fatalf("got %d entries, want 1", len(entries))
		}
		if byReading, _ := l.Find("たべる", "たべた"); len(byReading) != 2 {
			t.Errorf("kana lookup should match 食べる and 喰べる by reading, got %d", len(byReading))
		}
		e := entries[0]
		if e.Headwords[0].Term != "食べる" {
			t.Errorf("first entry = %s, want 食べる (exact term match)", e.Headwords[0].Term)
		}
		if e.Definitions[0].Dictionary != order[0] {
			t.Errorf("order %v: first definition from %s", order, e.Definitions[0].Dictionary)
		}
		if len(e.Definitions) != 3 {
			t.Errorf("definitions = %d, want 3", len(e.Definitions))
		}
		var a []*Definition
		for _, d := range e.Definitions {
			if d.Dictionary == "A" {
				a = append(a, d)
			}
		}
		if string(a[0].Entries[0]) != `"to eat (A)"` {
			t.Errorf("A definitions not sorted by score: %s", a[0].Entries[0])
		}
		if len(a[0].Tags) != 1 || a[0].Tags[0].Notes() != "Ichidan verb" || a[0].Tags[0].Redundant {
			t.Errorf("tag = %+v", a[0].Tags)
		}
		if !a[1].Tags[0].Redundant {
			t.Error("repeated part of speech tag should be redundant")
		}
		if len(e.Frequencies) != 1 || *e.Frequencies[0].DisplayValue != "500㋕" || e.Frequencies[0].FrequencyMode != "rank-based" {
			t.Errorf("frequencies = %+v", e.Frequencies)
		}
		if len(e.Pronunciations) != 1 || e.Pronunciations[0].Items[0].Positions.Position != 2 {
			t.Errorf("pronunciations = %+v", e.Pronunciations)
		}
	}
}

func TestFindFallsBackToSurface(t *testing.T) {
	st := setup(t)
	l, err := New(st, Options{Dictionaries: []settings.Dictionary{{Name: "A", Enabled: true}}, ResultOutputMode: "split"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := l.Find("存在しない", "食べる")
	if err != nil || len(entries) != 2 {
		t.Fatalf("got %d entries, err %v", len(entries), err)
	}
	if string(entries[0].Definitions[0].Entries[0]) != `"to eat (A)"` {
		t.Errorf("split mode should sort by score")
	}
	if entries, _ := l.Find("無い", "無い"); len(entries) != 0 {
		t.Error("expected no entries")
	}
}

func TestFindAt(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	importDict(t, st, map[string]string{
		"index.json": `{"title":"J","revision":"1","format":3}`,
		"term_bank_1.json": `[["労る","いたわる","v5","v5",0,["to pity"],1,""],["いたわる","いたわる","v5","v5",0,["to be kind"],2,""],
			["馴染む","なじむ","v5","v5",0,["to become familiar"],3,""],["馴染み","なじみ","n","n",0,["intimacy"],4,""],
			["休み","やすみ","n","n",0,["rest"],5,""],["休む","やすむ","v5","v5",0,["to rest"],6,""]]`,
	})
	d, err := deinflect.New()
	if err != nil {
		t.Fatal(err)
	}
	l, err := New(st, Options{Dictionaries: []settings.Dictionary{{Name: "J", Enabled: true}}, ResultOutputMode: "group"}, d)
	if err != nil {
		t.Fatal(err)
	}

	entries, err := l.FindAt("青子の健康をいたわって休みを入れる", 6)
	if err != nil || len(entries) == 0 {
		t.Fatalf("entries = %v, err = %v", entries, err)
	}
	e := entries[0]
	if e.Headwords[0].Term != "いたわる" || e.PrimarySource().OriginalText != "いたわって" {
		t.Errorf("got %s from %q", e.Headwords[0].Term, e.PrimarySource().OriginalText)
	}
	if len(e.InflectionChains) == 0 || len(e.InflectionChains[0]) == 0 {
		t.Errorf("expected an inflection chain, got %v", e.InflectionChains)
	}

	// Yomitan prefers the exact noun over the deinflected verb.
	entries, _ = l.FindAt("馴染みのない布団", 0)
	if len(entries) == 0 || entries[0].Headwords[0].Term != "馴染み" {
		t.Errorf("馴染み: %+v", entries)
	}
	// The noun must not match a verb inflection (part of speech filter).
	for _, e := range entries {
		if e.Headwords[0].Term == "馴染む" && e.PrimarySource().OriginalText == "馴染みの" {
			t.Error("unexpected match")
		}
	}

	if entries, _ := l.FindAt("休みを", 0); len(entries) == 0 || entries[0].Headwords[0].Term != "休み" {
		t.Errorf("休み: %+v", entries)
	}
	if entries, _ := l.FindAt("abc", 5); entries != nil {
		t.Error("out of range start should return nothing")
	}
}

func TestParseText(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	importDict(t, st, map[string]string{
		"index.json":       `{"title":"J","revision":"1","format":3}`,
		"term_bank_1.json": `[["食べる","たべる","v1","v1",0,["to eat"],1,""],["寿司","すし","n","n",0,["sushi"],2,""]]`,
	})
	d, _ := deinflect.New()
	l, err := New(st, Options{Dictionaries: []settings.Dictionary{{Name: "J", Enabled: true}}, ResultOutputMode: "group"}, d)
	if err != nil {
		t.Fatal(err)
	}
	terms, err := l.ParseText("「寿司を食べた」")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, term := range terms {
		s := ""
		for _, seg := range term {
			s += seg.Text
			if seg.Reading != "" {
				s += "[" + seg.Reading + "]"
			}
		}
		got = append(got, s)
	}
	want := []string{"「", "寿司[すし]", "を", "食[た]べた", "」"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %v, want %v", got, want)
	}
}
