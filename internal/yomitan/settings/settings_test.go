package settings

import "testing"

func TestParse(t *testing.T) {
	e, err := Parse([]byte(`{"version":1,"options":{"profileCurrent":1,"profiles":[
	 {"name":"Other","options":{}},
	 {"name":"Default","options":{
	  "dictionaries":[{"name":"JMdict","alias":"","enabled":true},{"name":"Off","enabled":false},{"name":"大辞林","alias":"DJ","enabled":true}],
	  "anki":{"server":"","tags":["yomitan"],"duplicateScope":"deck","fieldTemplates":null,
	   "cardFormats":[{"name":"Expression","type":"term","deck":"Mining","model":"Lapis",
	     "fields":{"Expression":{"value":"{expression}","overwriteMode":"coalesce"},"Glossary":{"value":"{glossary}","overwriteMode":"coalesce"}}}]},
	  "audio":{"enabled":true,"sources":[{"type":"jpod101","url":"","voice":""}]}}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := e.Profile("")
	if p.Name != "Default" {
		t.Fatalf("current profile = %q", p.Name)
	}
	en := p.Options.Dictionaries.Enabled()
	if len(en) != 2 || en[0].Name != "JMdict" || en[1].DisplayName() != "DJ" {
		t.Errorf("enabled = %+v", en)
	}
	f, err := p.Options.Anki.TermFormat()
	if err != nil {
		t.Fatal(err)
	}
	if f.Deck != "Mining" || len(f.Fields) != 2 || f.Fields[0].Name != "Expression" || f.Fields[1].Value != "{glossary}" {
		t.Errorf("format = %+v", f)
	}
	if p.Options.Anki.ServerURL() != "http://127.0.0.1:8765" || !p.Options.Anki.DuplicateCheck() || p.Options.Anki.HasCustomTemplates() {
		t.Error("anki defaults wrong")
	}
}

func TestLegacyShapes(t *testing.T) {
	e, err := Parse([]byte(`{"options":{"profiles":[{"name":"p","options":{
	 "dictionaries":{"B":{"priority":1,"enabled":true},"A":{"priority":5,"enabled":true}},
	 "anki":{"terms":{"deck":"D","model":"M","fields":{"Front":"{expression}","Back":"{glossary}"}}}}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := e.Profile("p")
	if d := p.Options.Dictionaries; d[0].Name != "A" || d[1].Name != "B" {
		t.Errorf("legacy dictionary order = %+v", d)
	}
	f, err := p.Options.Anki.TermFormat()
	if err != nil || f.Fields[1].Value != "{glossary}" {
		t.Errorf("legacy format = %+v %v", f, err)
	}
}
