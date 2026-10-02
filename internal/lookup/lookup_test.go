package lookup

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

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
		l, err := New(st, Options{Dictionaries: dicts, ResultOutputMode: "group"})
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
	l, err := New(st, Options{Dictionaries: []settings.Dictionary{{Name: "A", Enabled: true}}, ResultOutputMode: "split"})
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
