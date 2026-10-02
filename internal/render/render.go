// Package render fills Anki note fields from a dictionary entry by
// reproducing the output of Yomitan's default Anki field templates
// (ext/data/templates/default-anki-field-templates.handlebars) and the
// helpers of its AnkiTemplateRenderer. Yomitan is licensed under the GNU
// GPL v3, as is this program.
package render

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/xythh/ann2html/internal/japanese"
	"github.com/xythh/ann2html/internal/lookup"
)

// Context describes where the word was found.
type Context struct {
	Sentence       string
	SentenceOffset int    // rune offset of OriginalText within Sentence
	OriginalText   string // the text as it appears in the sentence
	DocumentTitle  string
	FullQuery      string
	URL            string
}

// Options are the Yomitan profile options that affect rendering.
type Options struct {
	ResultOutputMode string // group, merge, split or term
	// Dictionaries are the enabled dictionary titles, for per-dictionary markers.
	Dictionaries       []string
	GlossaryLayoutMode string
	CompactTags        bool
	// DictionaryStyles maps dictionary titles to their styles.css.
	DictionaryStyles map[string]string
}

// Note holds everything needed to render the fields of one note.
type Note struct {
	Entry   *lookup.Entry
	Context Context
	Options Options
	// Media returns Anki file names for dictionary images.
	Media MediaResolver
	// Audio returns the Anki file name of the term's audio, if any.
	Audio func() (string, bool)

	def *definition
}

var markerPattern = regexp.MustCompile(`\{([\p{L}\p{N}_-]+)\}`)

// Markers lists the markers used in a field template.
func Markers(template string) []string {
	var out []string
	for _, m := range markerPattern.FindAllStringSubmatch(template, -1) {
		out = append(out, m[1])
	}
	return out
}

// Field replaces every {marker} in template with its rendered value.
func (n *Note) Field(template string) string {
	return markerPattern.ReplaceAllStringFunc(template, func(m string) string {
		return n.Marker(m[1 : len(m)-1])
	})
}

// ---- note data (port of anki-note-data-creator.js) ----

type tagData struct {
	name, category, notes, dictionary string
	order, score                      float64
	redundant                         bool
}

func convertTag(t *lookup.Tag) tagData {
	d := tagData{name: t.Name, category: t.Category, notes: t.Notes(), order: t.Order, score: t.Score, redundant: t.Redundant}
	if len(t.Dictionaries) > 0 {
		d.dictionary = t.Dictionaries[0]
	}
	return d
}

type defData struct {
	dictionary, dictionaryAlias string
	glossaryScopedStyles        string
	dictScopedStyles            string
	glossary                    []json.RawMessage
	definitionTags              []tagData
	only                        []string
}

type expressionData struct {
	expression, reading string
	termFrequency       string
	wordClasses         []string
}

type definition struct {
	typ                  string // term, termGrouped, termMerged
	expressions          []string
	readings             []string
	dictionary           string
	dictionaryAlias      string
	glossary             []json.RawMessage // type "term" only
	glossaryScopedStyles string
	dictScopedStyles     string
	definitionTags       []tagData // all tags (type "term")
	definitions          []defData // grouped types
	termTags             []tagData
	exprs                []expressionData
}

func (n *Note) definition() *definition {
	if n.def != nil {
		return n.def
	}
	e := n.Entry
	d := &definition{typ: "term"}
	switch n.Options.ResultOutputMode {
	case "group", "term":
		d.typ = "termGrouped"
	case "merge":
		d.typ = "termMerged"
	}
	merged := d.typ == "termMerged"

	allTerms := map[string]bool{}
	allReadings := map[string]bool{}
	for _, h := range e.Headwords {
		if !allTerms[h.Term] {
			allTerms[h.Term] = true
			d.expressions = append(d.expressions, h.Term)
		}
		if !allReadings[h.Reading] {
			allReadings[h.Reading] = true
			d.readings = append(d.readings, h.Reading)
		}
	}
	for _, def := range e.Definitions {
		if d.dictionary == "" {
			d.dictionary, d.dictionaryAlias = def.Dictionary, def.DictionaryAlias
		}
		styles := sanitizeCSS(n.Options.DictionaryStyles[def.Dictionary])
		var glossaryScoped, dictScoped string
		if styles != "" {
			glossaryScoped = addGlossaryScope(styles)
			dictScoped = addGlossaryScope(addDictionaryScope(styles, def.Dictionary))
		}
		if n.Options.GlossaryLayoutMode == "compact-popup-anki" {
			dictScoped += addGlossaryScope(ankiCompactGlossStyles)
		}
		var tags []tagData
		for _, t := range def.Tags {
			tags = append(tags, convertTag(t))
		}
		d.definitionTags = append(d.definitionTags, tags...)
		if d.typ == "term" {
			d.glossary = append(d.glossary, def.Entries...)
			if styles != "" {
				d.glossaryScopedStyles += glossaryScoped
				d.dictScopedStyles += dictScoped
			}
			continue
		}
		dd := defData{
			dictionary: def.Dictionary, dictionaryAlias: def.DictionaryAlias,
			glossaryScopedStyles: glossaryScoped, dictScopedStyles: dictScoped,
			glossary: def.Entries, definitionTags: tags,
		}
		if merged {
			dd.only = disambiguations(e.Headwords, def.HeadwordIndices, d.expressions, d.readings)
		}
		d.definitions = append(d.definitions, dd)
	}
	if !merged {
		for _, t := range groupTermTags(e) {
			d.termTags = append(d.termTags, convertTag(t))
		}
	}
	for _, h := range e.Headwords {
		var score float64
		for _, t := range h.Tags {
			score += t.Score
		}
		freq := "normal"
		if score > 0 {
			freq = "popular"
		} else if score < 0 {
			freq = "rare"
		}
		d.exprs = append(d.exprs, expressionData{h.Term, h.Reading, freq, h.WordClasses})
	}
	n.def = d
	return d
}

func groupTermTags(e *lookup.Entry) []*lookup.Tag {
	var out []*lookup.Tag
	seen := map[string]bool{}
	unique := len(e.Headwords) > 1
	for _, h := range e.Headwords {
		for _, t := range h.Tags {
			if unique {
				key := t.Name + "\x00" + t.Category + "\x00" + strings.Join(t.Content, "\x01") + "\x00" + strings.Join(t.Dictionaries, "\x01")
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			out = append(out, t)
		}
	}
	return out
}

func disambiguations(headwords []*lookup.Headword, indices []int, allTerms, allReadings []string) []string {
	if len(allTerms) <= 1 && len(allReadings) <= 1 {
		return nil
	}
	var terms, readings []string
	for _, i := range indices {
		terms = appendUniqueStr(terms, headwords[i].Term)
		readings = appendUniqueStr(readings, headwords[i].Reading)
	}
	var out []string
	addTerms := !sameSet(terms, allTerms)
	addReadings := !sameSet(readings, allReadings)
	if addTerms {
		out = append(out, intersect(terms, allTerms)...)
	}
	if addReadings {
		if addTerms {
			var kept []string
			for _, r := range readings {
				if !containsStr(terms, r) {
					kept = append(kept, r)
				}
			}
			readings = kept
		}
		out = append(out, intersect(readings, allReadings)...)
	}
	return out
}

func appendUniqueStr(list []string, s string) []string {
	if containsStr(list, s) {
		return list
	}
	return append(list, s)
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, v := range a {
		if !containsStr(b, v) {
			return false
		}
	}
	return true
}

func intersect(a, b []string) []string {
	var out []string
	for _, v := range a {
		if containsStr(b, v) {
			out = append(out, v)
		}
	}
	return out
}

// ---- markers ----

// hbEscape is Handlebars' escapeExpression.
func hbEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#x27;", "`", "&#x60;", "=", "&#x3D;").Replace(s)
}

// escapeAngles is AnkiTemplateRenderer._escapeExpression.
func escapeAngles(s string) string {
	return strings.NewReplacer("<", "&lt;", ">", "&gt;").Replace(s)
}

func multiLine(s string) string { return strings.ReplaceAll(s, "\n", "<br>") }

// Marker renders a single marker. Unknown markers render as empty strings.
func (n *Note) Marker(marker string) string {
	d := n.definition()
	merge := d.typ == "termMerged"
	switch marker {
	case "audio":
		if n.Audio != nil {
			if name, ok := n.Audio(); ok {
				return "[sound:" + name + "]"
			}
		}
		return ""
	case "character", "kunyomi", "onyomi", "onyomi-hiragana", "screenshot", "clipboard-image",
		"clipboard-text", "popup-selection-text", "conjugation":
		return ""
	case "dictionary":
		return hbEscape(d.dictionary)
	case "dictionary-alias":
		return hbEscape(d.dictionaryAlias)
	case "expression":
		if merge {
			return strings.Join(d.expressions, "、")
		}
		return hbEscape(first(d.expressions))
	case "reading":
		if merge {
			return strings.Join(d.readings, "、")
		}
		return hbEscape(first(d.readings))
	case "furigana", "furigana-plain":
		f := furiganaHTML
		if marker == "furigana-plain" {
			f = func(e, r string) string { return hbEscape(furiganaPlain(e, r)) }
		}
		if merge {
			parts := make([]string, len(d.exprs))
			for i, x := range d.exprs {
				parts[i] = `<span class="expression-` + hbEscape(x.termFrequency) + `">` + f(x.expression, x.reading) + `</span>`
			}
			return strings.Join(parts, "、")
		}
		return f(first(d.expressions), first(d.readings))
	case "glossary":
		return n.glossary(false, false, false, "")
	case "glossary-brief":
		return n.glossary(true, false, false, "")
	case "glossary-no-dictionary":
		return n.glossary(false, true, false, "")
	case "glossary-first":
		return n.glossary(false, false, true, "")
	case "glossary-first-brief":
		return n.glossary(true, false, true, "")
	case "glossary-first-no-dictionary":
		return n.glossary(false, true, true, "")
	case "glossary-plain":
		return n.glossaryPlain(false, "")
	case "glossary-plain-no-dictionary":
		return n.glossaryPlain(true, "")
	case "sentence", "sentence-furigana", "sentence-furigana-plain":
		return n.Context.Sentence
	case "cloze-prefix", "cloze-body", "cloze-body-kana", "cloze-suffix":
		prefix, body, bodyKana, suffix := n.cloze()
		return map[string]string{"cloze-prefix": prefix, "cloze-body": body, "cloze-body-kana": bodyKana, "cloze-suffix": suffix}[marker]
	case "tags":
		return hbEscape(n.mergeTags())
	case "url":
		u := hbEscape(n.Context.URL)
		return `<a href="` + u + `">` + u + `</a>`
	case "url-plain":
		return hbEscape(n.Context.URL)
	case "document-title":
		return hbEscape(n.Context.DocumentTitle)
	case "search-query":
		return multiLine(hbEscape(n.Context.FullQuery))
	case "pitch-accents":
		return n.pitchAccentList("text")
	case "pitch-accent-graphs":
		return n.pitchAccentList("graph")
	case "pitch-accent-graphs-jj":
		return n.pitchAccentList("graph-jj")
	case "pitch-accent-positions":
		return n.pitchAccentList("position")
	case "pitch-accent-categories":
		return strings.Join(n.pitchCategories(), ",")
	case "phonetic-transcriptions":
		return n.phoneticTranscriptions()
	case "frequencies":
		return n.frequencies("")
	case "single-frequency-number":
		return n.singleFrequencyNumber("")
	case "frequency-harmonic-rank":
		return orDefault(frequencyHarmonic(n.Entry, "rank-based"), "9999999")
	case "frequency-harmonic-occurrence":
		return orDefault(frequencyHarmonic(n.Entry, "occurrence-based"), "0")
	case "frequency-average-rank":
		return orDefault(frequencyAverage(n.Entry, "rank-based"), "9999999")
	case "frequency-average-occurrence":
		return orDefault(frequencyAverage(n.Entry, "occurrence-based"), "0")
	case "stroke-count":
		return "Stroke count: Unknown"
	case "part-of-speech":
		return n.partOfSpeech()
	}
	if s, ok := n.dynamicMarker(marker); ok {
		return s
	}
	return ""
}

func first(list []string) string {
	if len(list) > 0 {
		return list[0]
	}
	return ""
}

func orDefault(v int64, def string) string {
	if v == -1 {
		return def
	}
	return strconv.FormatInt(v, 10)
}

func furiganaHTML(expression, reading string) string {
	var b strings.Builder
	for _, s := range japanese.DistributeFurigana(expression, reading) {
		if s.Reading != "" {
			b.WriteString("<ruby>" + s.Text + "<rt>" + s.Reading + "</rt></ruby>")
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

func furiganaPlain(expression, reading string) string {
	var b strings.Builder
	for _, s := range japanese.DistributeFurigana(expression, reading) {
		if s.Reading != "" {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(s.Text + "[" + s.Reading + "]")
		} else {
			b.WriteString(s.Text)
		}
	}
	return b.String()
}

// glossarySingle renders the "glossary-single" partial. prev carries the
// "previousDictionary" template state between definitions.
func (n *Note) glossarySingle(dictionary, alias string, tags []tagData, glossary []json.RawMessage, only []string, brief, noDictionaryTag bool, prev *prevDict) string {
	var b strings.Builder
	compactTags := n.Options.CompactTags
	if !brief {
		anyTag := false
		add := func(s string) {
			if anyTag {
				b.WriteString(", ")
			} else {
				b.WriteString("<i>(")
			}
			b.WriteString(hbEscape(s))
			anyTag = true
		}
		for _, t := range tags {
			if !compactTags || !t.redundant {
				add(t.name)
			}
		}
		if !noDictionaryTag && (!compactTags || !prev.set || dictionary != prev.value) {
			add(alias)
		}
		if anyTag {
			b.WriteString(")</i> ")
		}
		if len(only) > 0 {
			b.WriteString("(")
			for i, o := range only {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(hbEscape(o))
			}
			b.WriteString(" only) ")
		}
	}
	switch {
	case len(glossary) <= 1:
		for _, g := range glossary {
			b.WriteString(n.formatGlossary(dictionary, g))
		}
	case n.Options.GlossaryLayoutMode == "compact-popup-anki":
		for i, g := range glossary {
			if i > 0 {
				b.WriteString(" | ")
			}
			b.WriteString(n.formatGlossary(dictionary, g))
		}
	default:
		b.WriteString("<ul>")
		for _, g := range glossary {
			b.WriteString("<li>" + n.formatGlossary(dictionary, g) + "</li>")
		}
		b.WriteString("</ul>")
	}
	prev.set, prev.value = true, dictionary
	return b.String()
}

// skip reports whether a definition is filtered out by selectedDictionary.
func skip(selected, dictionary string) bool { return selected != "" && selected != dictionary }

func (n *Note) glossary(brief, noDictionaryTag, firstOnly bool, selected string) string {
	d := n.definition()
	var b strings.Builder
	b.WriteString(`<div style="text-align: left;" class="yomitan-glossary">`)
	prev := &prevDict{}
	switch {
	case d.typ == "term":
		if !firstOnly && skip(selected, d.dictionary) {
			break
		}
		b.WriteString(n.glossarySingle(d.dictionary, d.dictionaryAlias, d.definitionTags, d.glossary, nil, brief, noDictionaryTag, prev))
		if d.glossaryScopedStyles != "" {
			b.WriteString("<style>" + d.glossaryScopedStyles + "</style>")
		}
	case firstOnly:
		if len(d.definitions) > 0 {
			dd := d.definitions[0]
			b.WriteString(n.glossarySingle(dd.dictionary, dd.dictionaryAlias, dd.definitionTags, dd.glossary, dd.only, brief, noDictionaryTag, prev))
			if dd.glossaryScopedStyles != "" {
				b.WriteString("<style>" + dd.glossaryScopedStyles + "</style>")
			}
		}
	default:
		b.WriteString("<ol>")
		for _, dd := range d.definitions {
			if skip(selected, dd.dictionary) {
				continue
			}
			b.WriteString(`<li data-dictionary="` + hbEscape(dd.dictionary) + `">`)
			b.WriteString(n.glossarySingle(dd.dictionary, dd.dictionaryAlias, dd.definitionTags, dd.glossary, dd.only, brief, noDictionaryTag, prev))
			b.WriteString("</li>")
			if dd.dictScopedStyles != "" {
				b.WriteString("<style>" + dd.dictScopedStyles + "</style>")
			}
		}
		b.WriteString("</ol>")
	}
	b.WriteString("</div>")
	return b.String()
}

// prevDict is the "previousDictionary" template state shared by the
// glossary-single partials of one glossary.
type prevDict struct {
	set   bool
	value string
}

var (
	kebabSpace   = regexp.MustCompile(`[\s_\x{3000}]`)
	kebabInvalid = regexp.MustCompile(`[^\p{L}\p{N}-]`)
	kebabDashes  = regexp.MustCompile(`--+`)
	kebabEdges   = regexp.MustCompile(`^-|-$`)
)

// kebabCase is Yomitan's getKebabCase, used to name per-dictionary markers.
func kebabCase(s string) string {
	s = kebabSpace.ReplaceAllString(s, "-")
	s = kebabInvalid.ReplaceAllString(s, "")
	s = kebabDashes.ReplaceAllString(s, "-")
	return strings.ToLower(kebabEdges.ReplaceAllString(s, ""))
}

// dynamicMarker renders the per-dictionary markers Yomitan generates for
// each enabled dictionary (anki-template-util.js getDynamicTemplates).
func (n *Note) dynamicMarker(marker string) (string, bool) {
	for _, dict := range n.Options.Dictionaries {
		k := kebabCase(dict)
		switch marker {
		case "single-glossary-" + k:
			return n.glossary(false, false, false, dict), true
		case "single-glossary-" + k + "-no-dictionary":
			return n.glossary(false, true, false, dict), true
		case "single-glossary-" + k + "-brief":
			return n.glossary(true, false, false, dict), true
		case "single-glossary-" + k + "-plain":
			return n.glossaryPlain(false, dict), true
		case "single-glossary-" + k + "-plain-no-dictionary":
			return n.glossaryPlain(true, dict), true
		case "single-frequency-number-" + k:
			return n.singleFrequencyNumber(dict), true
		case "single-frequency-" + k:
			return n.frequencies(dict), true
		}
	}
	return "", false
}

func (n *Note) glossaryPlain(noDictionaryTag bool, selected string) string {
	d := n.definition()
	var b strings.Builder
	if d.typ == "term" {
		if skip(selected, d.dictionary) {
			return ""
		}
		if !noDictionaryTag {
			b.WriteString("(" + hbEscape(d.dictionaryAlias) + ")<br>")
		}
		for _, g := range d.glossary {
			b.WriteString(n.formatGlossaryPlain(d.dictionary, g))
		}
		return b.String()
	}
	for i, dd := range d.definitions {
		if skip(selected, dd.dictionary) {
			continue
		}
		if !noDictionaryTag {
			b.WriteString("(" + hbEscape(dd.dictionaryAlias) + ")<br>")
		}
		for j, g := range dd.glossary {
			b.WriteString(n.formatGlossaryPlain(dd.dictionary, g))
			if j < len(dd.glossary)-1 {
				b.WriteString("<br>")
			}
		}
		if i < len(d.definitions)-1 {
			b.WriteString("<br>")
		}
	}
	return b.String()
}

type glossaryObject struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

func (n *Note) generator() *structuredContentGenerator {
	return &structuredContentGenerator{media: n.Media}
}

func (n *Note) formatGlossary(dictionary string, raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return multiLine(escapeAngles(s))
	}
	var obj glossaryObject
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	switch obj.Type {
	case "image":
		var img imageData
		json.Unmarshal(raw, &img)
		return html(n.generator().createDefinitionImage(img, dictionary), structuredContentStyles, keepStructuredContentData)
	case "structured-content":
		return html(n.generator().createStructuredContent(obj.Content, dictionary), structuredContentStyles, keepStructuredContentData)
	case "text":
		return multiLine(escapeAngles(obj.Text))
	}
	return ""
}

func (n *Note) formatGlossaryPlain(dictionary string, raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return escapeAngles(s)
	}
	var obj glossaryObject
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	switch obj.Type {
	case "structured-content":
		if parts := n.extractGlossaryData(obj.Content); len(parts) > 0 {
			return strings.Join(parts, "<br>\n")
		}
		return structuredText(n.generator().createStructuredContent(obj.Content, dictionary))
	case "text":
		return escapeAngles(obj.Text)
	}
	return ""
}

var (
	textBlockTags  = regexp.MustCompile(`<(div|li|ol|ul|br|details|summary|hr)(\s.*?>|>)`)
	textInlineTags = regexp.MustCompile(`<(span|a|ruby)(\s.*?>|>)`)
	textRtStart    = regexp.MustCompile(`<rt(\s.*?>|>)`)
	textAnyTag     = regexp.MustCompile(`(?s)<.*?>`)
	textNewlines   = regexp.MustCompile(`\n+`)
	textLeading    = regexp.MustCompile(`^(\s*<br>\s*|\s)*`)
)

// structuredText is AnkiTemplateRenderer._getText.
func structuredText(n *node) string {
	s := html(n, structuredContentStyles, keepStructuredContentData)
	s = textBlockTags.ReplaceAllString(s, "\n")
	s = textInlineTags.ReplaceAllString(s, " ")
	s = textRtStart.ReplaceAllString(s, "[")
	s = strings.ReplaceAll(s, "</rt>", "]")
	s = textAnyTag.ReplaceAllString(s, "")
	s = escapeAngles(s)
	s = textNewlines.ReplaceAllString(s, "<br>")
	s = textLeading.ReplaceAllString(s, "")
	return strings.ReplaceAll(s, "<br>", "<br>\n")
}

type scNode struct {
	Tag     string          `json:"tag"`
	Content json.RawMessage `json:"content"`
	Data    struct {
		Content string `json:"content"`
	} `json:"data"`
}

func asArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	var list []json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return list, true
	}
	return nil, false
}

func truthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null" && s != "false" && s != `""` && s != "0"
}

func (n *Note) extractGlossaryData(content json.RawMessage) []string {
	var queue []json.RawMessage
	var extract func(items []json.RawMessage)
	extract = func(items []json.RawMessage) {
		for _, it := range items {
			if list, ok := asArray(it); ok {
				extract(list)
				continue
			}
			var obj scNode
			if json.Unmarshal(it, &obj) != nil {
				continue
			}
			if obj.Data.Content == "glossary" {
				queue = append(queue, it)
				continue
			}
			if truthy(obj.Content) {
				extract([]json.RawMessage{obj.Content})
			}
		}
	}
	extract([]json.RawMessage{content})

	var out []string
	var convert func(items []json.RawMessage)
	convert = func(items []json.RawMessage) {
		for _, it := range items {
			var s string
			if json.Unmarshal(it, &s) == nil {
				out = append(out, s)
				continue
			}
			if list, ok := asArray(it); ok {
				convert(list)
				continue
			}
			var obj scNode
			if json.Unmarshal(it, &obj) != nil || !truthy(obj.Content) {
				continue
			}
			if obj.Tag == "ruby" {
				out = append(out, structuredText(n.generator().createStructuredContent(obj.Content, "")))
				continue
			}
			convert([]json.RawMessage{obj.Content})
		}
	}
	convert(queue)
	return out
}

func (n *Note) cloze() (prefix, body, bodyKana, suffix string) {
	text := []rune(n.Context.Sentence)
	offset := n.Context.SentenceOffset
	if offset < 0 || offset > len(text) {
		offset = 0
	}
	end := offset + len([]rune(n.Context.OriginalText))
	if end > len(text) {
		end = len(text)
	}
	prefix, body, suffix = string(text[:offset]), string(text[offset:end]), string(text[end:])
	var term, reading string
	if len(n.Entry.Headwords) > 0 {
		term, reading = n.Entry.Headwords[0].Term, n.Entry.Headwords[0].Reading
	}
	var kb strings.Builder
	for _, s := range japanese.DistributeFuriganaInflected(term, reading, body) {
		if s.Reading != "" {
			kb.WriteString(s.Reading)
		} else {
			kb.WriteString(s.Text)
		}
	}
	return prefix, body, kb.String(), suffix
}

func (n *Note) mergeTags() string {
	d := n.definition()
	var names []string
	for _, t := range d.termTags {
		names = appendUniqueStr(names, t.name)
	}
	if d.typ != "term" {
		for _, dd := range d.definitions {
			for _, t := range dd.definitionTags {
				names = appendUniqueStr(names, t.name)
			}
		}
	} else {
		for _, t := range d.definitionTags {
			names = appendUniqueStr(names, t.name)
		}
	}
	return strings.Join(names, ", ")
}

func (n *Note) partOfSpeech() string {
	d := n.definition()
	var b strings.Builder
	firstItem := true
	used := map[string]bool{}
	for _, x := range d.exprs {
		for _, wc := range x.wordClasses {
			if used[wc] {
				continue
			}
			b.WriteString(prettyPartOfSpeech(wc))
			if !firstItem {
				b.WriteString(", ")
			}
			used[wc] = true
			firstItem = false
		}
	}
	if firstItem {
		return "Unknown"
	}
	return b.String()
}

func prettyPartOfSpeech(wc string) string {
	switch wc {
	case "v1":
		return "Ichidan verb"
	case "v5":
		return "Godan verb"
	case "vk":
		return "Kuru verb"
	case "vs":
		return "Suru verb"
	case "vz":
		return "Zuru verb"
	case "adj-i":
		return "I-adjective"
	case "n":
		return "Noun"
	}
	return hbEscape(wc)
}

// ---- frequencies ----

func (n *Note) frequencies(selected string) string {
	e := n.Entry
	if len(e.Frequencies) == 0 {
		return ""
	}
	d := n.definition()
	multi := len(d.expressions) > 1 || len(d.readings) > 1
	var b strings.Builder
	b.WriteString(`<ul style="text-align: left;">`)
	for _, f := range e.Frequencies {
		if skip(selected, f.Dictionary) {
			continue
		}
		b.WriteString("<li>")
		if multi {
			h := e.Headwords[f.HeadwordIndex]
			b.WriteString("(" + furiganaHTML(h.Term, h.Reading) + ") ")
		}
		value := jsNumber(f.Frequency)
		if f.DisplayValue != nil {
			value = *f.DisplayValue
		}
		b.WriteString(hbEscape(f.DictionaryAlias) + ": " + hbEscape(value) + "</li>")
	}
	b.WriteString("</ul>")
	return b.String()
}

func (n *Note) singleFrequencyNumber(selected string) string {
	var b strings.Builder
	for _, f := range frequencyNumbers(n.Entry, "") {
		if !skip(selected, f.dictionary) {
			b.WriteString(hbEscape(jsNumber(f.frequency)))
		}
	}
	return b.String()
}

type frequencyNumber struct {
	dictionary string
	frequency  float64
}

var leadingDigits = regexp.MustCompile(`^\d+`)

func frequencyNumbers(e *lookup.Entry, mode string) []frequencyNumber {
	var out []frequencyNumber
	prev := "\x00none"
	for _, f := range e.Frequencies {
		if f.Dictionary == prev || (mode != "" && f.FrequencyMode != "" && f.FrequencyMode != mode) {
			continue
		}
		prev = f.Dictionary
		if f.DisplayValue != nil {
			if m := leadingDigits.FindString(*f.DisplayValue); m != "" {
				if v, _ := strconv.ParseFloat(m, 64); v > 0 {
					out = append(out, frequencyNumber{f.Dictionary, v})
					continue
				}
			}
		}
		if f.Frequency > 0 {
			out = append(out, frequencyNumber{f.Dictionary, f.Frequency})
		}
	}
	return out
}

func frequencyHarmonic(e *lookup.Entry, mode string) int64 {
	nums := frequencyNumbers(e, mode)
	if len(nums) == 0 {
		return -1
	}
	var total float64
	for _, f := range nums {
		total += 1 / f.frequency
	}
	return int64(math.Floor(float64(len(nums)) / total))
}

func frequencyAverage(e *lookup.Entry, mode string) int64 {
	nums := frequencyNumbers(e, mode)
	if len(nums) == 0 {
		return -1
	}
	var total float64
	for _, f := range nums {
		total += f.frequency
	}
	return int64(math.Floor(total / float64(len(nums))))
}

// ---- pronunciations ----

type groupedPronunciation struct {
	item              lookup.PronunciationItem
	terms             []string
	reading           string
	exclusiveTerms    []string
	exclusiveReadings []string
}

type dictionaryPronunciations struct {
	dictionary string
	items      []groupedPronunciation
}

func tagListsEqual(a, b []*lookup.Tag) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || strings.Join(a[i].Dictionaries, "\x00") != strings.Join(b[i].Dictionaries, "\x00") {
			return false
		}
	}
	return true
}

func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pronunciationsEquivalent(a, b lookup.PronunciationItem) bool {
	if a.Type != b.Type || !tagListsEqual(a.Tags, b.Tags) {
		return false
	}
	switch a.Type {
	case "pitch-accent":
		return a.Positions == b.Positions && intsEqual(a.NasalPositions, b.NasalPositions) && intsEqual(a.DevoicePositions, b.DevoicePositions)
	case "phonetic-transcription":
		return a.IPA == b.IPA
	}
	return true
}

// groupedPronunciations ports getGroupedPronunciations.
func (n *Note) groupedPronunciations() []dictionaryPronunciations {
	e := n.Entry
	var allTerms, allReadings []string
	for _, h := range e.Headwords {
		allTerms = appendUniqueStr(allTerms, h.Term)
		allReadings = appendUniqueStr(allReadings, h.Reading)
	}
	var out []dictionaryPronunciations
	index := map[string]int{}
	for _, p := range e.Pronunciations {
		h := e.Headwords[p.HeadwordIndex]
		i, ok := index[p.Dictionary]
		if !ok {
			i = len(out)
			index[p.Dictionary] = i
			out = append(out, dictionaryPronunciations{dictionary: p.Dictionary})
		}
		group := &out[i]
		for _, item := range p.Items {
			found := -1
			for j, g := range group.items {
				if g.reading == h.Reading && pronunciationsEquivalent(g.item, item) {
					found = j
					break
				}
			}
			if found < 0 {
				group.items = append(group.items, groupedPronunciation{item: item, reading: h.Reading})
				found = len(group.items) - 1
			}
			group.items[found].terms = appendUniqueStr(group.items[found].terms, h.Term)
		}
	}
	for i := range out {
		for j := range out[i].items {
			g := &out[i].items[j]
			if !sameSet(g.terms, allTerms) {
				g.exclusiveTerms = intersect(g.terms, allTerms)
			}
			if len(allReadings) > 1 {
				g.exclusiveReadings = []string{g.reading}
			}
		}
	}
	return out
}

func (n *Note) pitchAccentList(format string) string {
	var pitches []groupedPronunciation
	for _, d := range n.groupedPronunciations() {
		for _, g := range d.items {
			if g.item.Type == "pitch-accent" {
				pitches = append(pitches, g)
			}
		}
	}
	count := len(pitches)
	if count == 0 {
		return ""
	}
	var b strings.Builder
	if count > 1 {
		b.WriteString("<ol>")
	}
	for _, p := range pitches {
		if count > 1 {
			b.WriteString("<li>")
		}
		exclusive := append(append([]string{}, p.exclusiveTerms...), p.exclusiveReadings...)
		if len(exclusive) > 0 {
			b.WriteString("<em>(" + strings.Join(exclusive, "") + " only) </em>")
		}
		b.WriteString(pronunciationHTML(format, p.reading, p.item))
		if count > 1 {
			b.WriteString("</li>")
		}
	}
	if count > 1 {
		b.WriteString("</ol>")
	}
	return b.String()
}

func pronunciationHTML(format, reading string, item lookup.PronunciationItem) string {
	if reading == "" {
		return ""
	}
	morae := japanese.GetKanaMorae(reading)
	switch format {
	case "text":
		return html(pronunciationText(morae, item.Positions, item.NasalPositions, item.DevoicePositions), pronunciationStyles, nil)
	case "graph":
		return html(pronunciationGraph(morae, item.Positions), pronunciationStyles, nil)
	case "position":
		return html(pronunciationDownstepPosition(item.Positions), pronunciationStyles, nil)
	}
	// graph-jj is not supported.
	return ""
}

func isNonNounVerbOrAdjective(wordClasses []string) bool {
	verbOrAdj, suru, noun := false, false, false
	for _, wc := range wordClasses {
		switch wc {
		case "v1", "v5", "vk", "vz", "adj-i":
			verbOrAdj = true
		case "vs":
			verbOrAdj, suru = true, true
		case "n":
			noun = true
		}
	}
	return verbOrAdj && !(suru && noun)
}

func (n *Note) pitchCategories() []string {
	e := n.Entry
	var out []string
	for _, p := range e.Pronunciations {
		h := e.Headwords[p.HeadwordIndex]
		isVerbOrAdj := isNonNounVerbOrAdjective(h.WordClasses)
		for _, item := range p.Items {
			if item.Type != "pitch-accent" {
				continue
			}
			if c := japanese.GetPitchCategory(h.Reading, item.Positions, isVerbOrAdj); c != "" {
				out = appendUniqueStr(out, c)
			}
		}
	}
	return out
}

func (n *Note) phoneticTranscriptions() string {
	e := n.Entry
	if len(e.Pronunciations) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<ul>")
	for _, p := range e.Pronunciations {
		for _, item := range p.Items {
			if item.Type != "phonetic-transcription" {
				continue
			}
			b.WriteString(`<li class="pronunciation" data-pronunciation-type="phonetic-transcription">`)
			for i, t := range item.Tags {
				if i > 0 {
					b.WriteString(", ")
				} else {
					b.WriteString("<i>(")
				}
				name := hbEscape(t.Name)
				b.WriteString(`<span class="tag" data-details="` + name + `">` + name + "</span>")
			}
			if len(item.Tags) > 0 {
				b.WriteString(")</i> ")
			}
			b.WriteString(hbEscape(item.IPA) + "</li>")
		}
	}
	b.WriteString("</ul>")
	return b.String()
}
