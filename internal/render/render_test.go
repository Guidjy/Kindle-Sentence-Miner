package render

import (
	"encoding/json"
	"testing"

	"github.com/xythh/ann2html/internal/japanese"
	"github.com/xythh/ann2html/internal/lookup"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func testEntry() *lookup.Entry {
	display := "500"
	return &lookup.Entry{
		Headwords: []*lookup.Headword{{Term: "食べる", Reading: "たべる", WordClasses: []string{"v1"},
			Sources: []lookup.Source{{OriginalText: "食べた", DeinflectedText: "食べる", IsPrimary: true, MatchSource: "term"}}}},
		Definitions: []*lookup.Definition{
			{Dictionary: "JMdict", DictionaryAlias: "JMdict", HeadwordIndices: []int{0},
				Tags:    []*lookup.Tag{{Name: "v1", Category: "partOfSpeech", Dictionaries: []string{"JMdict"}}},
				Entries: []json.RawMessage{raw(`"to eat"`), raw(`"to live on"`)}},
			{Dictionary: "Mono", DictionaryAlias: "国語", HeadwordIndices: []int{0},
				Entries: []json.RawMessage{raw(`{"type":"structured-content","content":{"tag":"ul","data":{"content":"glossary"},"content":[{"tag":"li","content":"食物を口に入れる"}]}}`)}},
		},
		Frequencies: []*lookup.Frequency{{Dictionary: "JPDB", DictionaryAlias: "JPDB", Frequency: 500, DisplayValue: &display, FrequencyMode: "rank-based"}},
		Pronunciations: []*lookup.Pronunciation{{Dictionary: "Pitch", DictionaryAlias: "Pitch",
			Items: []lookup.PronunciationItem{{Type: "pitch-accent", Positions: japanese.Pitch{Position: 2}}}}},
	}
}

func testNote(mode string) *Note {
	return &Note{
		Entry: testEntry(),
		Context: Context{Sentence: "昨日寿司を食べた。", SentenceOffset: 5, OriginalText: "食べた",
			DocumentTitle: "本 & ノート", FullQuery: "食べた"},
		Options: Options{ResultOutputMode: mode, Dictionaries: []string{"JMdict", "Mono"}},
	}
}

func TestGlossaryGrouped(t *testing.T) {
	got := testNote("group").Marker("glossary")
	want := `<div style="text-align: left;" class="yomitan-glossary"><ol>` +
		`<li data-dictionary="JMdict"><i>(v1, JMdict)</i> <ul><li>to eat</li><li>to live on</li></ul></li>` +
		`<li data-dictionary="Mono"><i>(国語)</i> <span><ul data-sc-content="glossary"><li lang="ja">食物を口に入れる</li></ul></span></li>` +
		`</ol></div>`
	if got != want {
		t.Errorf("glossary:\n got %s\nwant %s", got, want)
	}
}

func TestGlossarySplitAndBrief(t *testing.T) {
	n := testNote("split")
	n.Entry.Definitions = n.Entry.Definitions[:1]
	got := n.Marker("glossary-brief")
	want := `<div style="text-align: left;" class="yomitan-glossary"><ul><li>to eat</li><li>to live on</li></ul></div>`
	if got != want {
		t.Errorf("glossary-brief:\n got %s\nwant %s", got, want)
	}
}

func TestSingleGlossaryMarker(t *testing.T) {
	got := testNote("group").Field("{single-glossary-mono-no-dictionary}")
	want := `<div style="text-align: left;" class="yomitan-glossary"><ol><li data-dictionary="Mono"><span><ul data-sc-content="glossary"><li lang="ja">食物を口に入れる</li></ul></span></li></ol></div>`
	if got != want {
		t.Errorf("single glossary:\n got %s\nwant %s", got, want)
	}
}

func TestSimpleMarkers(t *testing.T) {
	n := testNote("group")
	cases := map[string]string{
		"{expression}":                   "食べる",
		"{reading}":                      "たべる",
		"{furigana}":                     "<ruby>食<rt>た</rt></ruby>べる",
		"{furigana-plain}":               "食[た]べる",
		"{sentence}":                     "昨日寿司を食べた。",
		"{cloze-prefix}":                 "昨日寿司を",
		"{cloze-body}":                   "食べた",
		"{cloze-body-kana}":              "たべた",
		"{cloze-suffix}":                 "。",
		"{document-title}":               "本 &amp; ノート",
		"{frequencies}":                  `<ul style="text-align: left;"><li>JPDB: 500</li></ul>`,
		"{frequency-harmonic-rank}":      "500",
		"{frequency-average-occurrence}": "0",
		"{pitch-accent-positions}":       `<span style="display:inline;"><span>[</span><span>2</span><span>]</span></span>`,
		"{pitch-accent-categories}":      "kifuku",
		"{part-of-speech}":               "Ichidan verb",
		"{tags}":                         "v1",
		"{dictionary}":                   "JMdict",
		"{unknown-marker}":               "",
		"<b>{expression}</b>":            "<b>食べる</b>",
	}
	for tmpl, want := range cases {
		if got := n.Field(tmpl); got != want {
			t.Errorf("%s = %q, want %q", tmpl, got, want)
		}
	}
}

func TestPitchText(t *testing.T) {
	got := testNote("group").Marker("pitch-accents")
	mora := func(high, highNext bool, c string) string {
		s := `<span style="display:inline-block;position:relative;`
		if high && !highNext {
			s += `padding-right:0.1em;margin-right:0.1em;`
		}
		s += `"><span style="display:inline;">` + c + `</span><span style="border-color:currentColor;`
		if high {
			s += `display:block;user-select:none;pointer-events:none;position:absolute;top:0.1em;left:0;right:0;height:0;border-top-width:0.1em;border-top-style:solid;`
			if !highNext {
				s += `right:-0.1em;height:0.4em;border-right-width:0.1em;border-right-style:solid;`
			}
		}
		return s + `"></span></span>`
	}
	want := `<span style="display:inline;">` + mora(false, true, "た") + mora(true, false, "べ") + mora(false, false, "る") + `</span>`
	if got != want {
		t.Errorf("pitch-accents:\n got %s\nwant %s", got, want)
	}
}

func TestStructuredContentTableAndImage(t *testing.T) {
	var uploaded []string
	n := testNote("split")
	n.Media = func(dict, path string) (string, bool) {
		uploaded = append(uploaded, dict+"/"+path)
		return "yomitan_dictionary_media_abc.png", true
	}
	n.Entry.Definitions = []*lookup.Definition{{Dictionary: "D", DictionaryAlias: "D", Entries: []json.RawMessage{
		raw(`{"type":"structured-content","content":[{"tag":"table","content":{"tag":"tr","content":{"tag":"td","content":"x","style":{"fontWeight":"bold"}}}},{"tag":"img","path":"a.png","width":10,"height":20}]}`),
	}}}
	got := n.Marker("glossary-no-dictionary")
	want := `<div style="text-align: left;" class="yomitan-glossary"><span>` +
		`<div style="display:block;"><table style="table-layout:auto;border-collapse:collapse;"><tr><td style="border-style:solid;padding:0.25em;vertical-align:top;border-width:1px;border-color:currentColor;font-weight: bold;">x</td></tr></table></div>` +
		`<a target="_blank" rel="noreferrer noopener" href="yomitan_dictionary_media_abc.png" style="cursor:inherit;display:inline-block;position:relative;line-height:1;max-width:100%;color:inherit;">` +
		`<span style="display:inline-block;white-space:nowrap;max-width:100%;max-height:100vh;position:relative;vertical-align:top;line-height:0;overflow:hidden;font-size:1px;width: 10em;">` +
		`<span style="display:inline-block;width:0;vertical-align:top;font-size:0;padding-top: 200%;"></span>` +
		`<span style="--image:none;position:absolute;left:0;top:0;width:100%;height:100%;-webkit-mask-repeat:no-repeat;-webkit-mask-position:center center;-webkit-mask-mode:alpha;-webkit-mask-size:contain;-webkit-mask-image:var(--image);mask-repeat:no-repeat;mask-position:center center;mask-mode:alpha;mask-size:contain;mask-image:var(--image);background-color:currentColor;display:none;--image: url(&quot;yomitan_dictionary_media_abc.png&quot;);"></span>` +
		`<span style="position:absolute;left:0;top:0;width:100%;height:100%;font-size:calc(1em * var(--font-size-no-units));line-height:var(--line-height);display:table;table-layout:fixed;white-space:normal;color:var(--text-color-light3);"></span>` +
		`<img width="10" height="20" style="display:inline-block;vertical-align:top;object-fit:contain;border:none;outline:none;position:absolute;left:0;top:0;width:100%;height:100%;width: 100%; height: 100%;" src="yomitan_dictionary_media_abc.png">` +
		`</span><span style="display:none;line-height:var(--line-height);">Image</span></a>` +
		`</span></div>`
	if got != want {
		t.Errorf("structured content:\n got %s\nwant %s", got, want)
	}
	if len(uploaded) != 1 || uploaded[0] != "D/a.png" {
		t.Errorf("uploaded = %v", uploaded)
	}
}

func TestScopedStyles(t *testing.T) {
	n := testNote("group")
	n.Entry.Definitions = n.Entry.Definitions[1:]
	n.Options.DictionaryStyles = map[string]string{"Mono": "/* c */ .a, .b > span { color: red ; }\n@media print { .a { color: blue } }"}
	got := n.Marker("glossary")
	want := `<div style="text-align: left;" class="yomitan-glossary"><ol><li data-dictionary="Mono"><i>(国語)</i> <span><ul data-sc-content="glossary"><li lang="ja">食物を口に入れる</li></ul></span></li>` +
		`<style>.yomitan-glossary [data-dictionary="Mono"] .a, .yomitan-glossary [data-dictionary="Mono"] .b > span { color: red; }</style></ol></div>`
	if got != want {
		t.Errorf("scoped styles:\n got %s\nwant %s", got, want)
	}
}

func TestKebabCase(t *testing.T) {
	if got := kebabCase("JMdict (English)_v2  Test"); got != "jmdict-english-v2-test" {
		t.Errorf("kebab = %q", got)
	}
}
