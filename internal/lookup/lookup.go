package lookup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/xythh/ann2html/internal/deinflect"
	"github.com/xythh/ann2html/internal/japanese"
	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

// Options are the Yomitan profile settings that affect lookups.
type Options struct {
	Dictionaries                 []settings.Dictionary // enabled, in priority order
	ResultOutputMode             string                // group, merge, split or term
	MainDictionary               string
	SortFrequencyDictionary      string
	SortFrequencyDictionaryOrder string
	ScanLength                   int // maximum characters scanned by FindAt
}

// OptionsFromProfile extracts lookup options from a Yomitan profile.
func OptionsFromProfile(p *settings.Profile) Options {
	g := p.Options.General
	mode := g.ResultOutputMode
	if mode == "" {
		mode = "group"
	}
	return Options{
		Dictionaries:                 p.Options.Dictionaries.Enabled(),
		ResultOutputMode:             mode,
		MainDictionary:               g.MainDictionary,
		SortFrequencyDictionary:      g.SortFrequencyDictionary,
		SortFrequencyDictionaryOrder: g.SortFrequencyDictionaryOrder,
		ScanLength:                   p.Options.Scanning.ScanLength(),
	}
}

// DictionaryInfo is an enabled dictionary that has been imported.
type DictionaryInfo struct {
	ID            int64
	Title         string
	Alias         string
	Index         int
	Styles        string
	FrequencyMode string
	Secondary     bool
	POSFilter     bool // deinflections must match the entry's part of speech
}

// Lookup searches the imported dictionaries.
type Lookup struct {
	st         *store.Store
	opts       Options
	deinflect  *deinflect.Deinflector
	byTitle    map[string]*DictionaryInfo
	byID       map[int64]*DictionaryInfo
	idList     string // comma separated ids for SQL IN clauses
	missing    []string
	tags       map[string]Tag // tag bank cache, keyed by dictionary + "\x00" + name
	conditions map[string]int // part of speech condition flags, keyed by rules
}

// New prepares a lookup over the enabled dictionaries of a profile. d may be
// nil, in which case FindAt does not deinflect.
func New(st *store.Store, opts Options, d *deinflect.Deinflector) (*Lookup, error) {
	l := &Lookup{st: st, opts: opts, deinflect: d, byTitle: map[string]*DictionaryInfo{}, byID: map[int64]*DictionaryInfo{},
		tags: map[string]Tag{}, conditions: map[string]int{}}
	if l.opts.ScanLength <= 0 {
		l.opts.ScanLength = 16
	}
	type row struct {
		id                    int64
		styles, frequencyMode string
	}
	rows, err := st.DB.Query(`SELECT id, title, styles, frequency_mode FROM dictionaries`)
	if err != nil {
		return nil, err
	}
	imported := map[string]row{}
	for rows.Next() {
		var r row
		var title string
		if err := rows.Scan(&r.id, &title, &r.styles, &r.frequencyMode); err != nil {
			rows.Close()
			return nil, err
		}
		imported[title] = r
	}
	rows.Close()

	var ids []string
	for i, d := range opts.Dictionaries {
		r, ok := imported[d.Name]
		if !ok {
			l.missing = append(l.missing, d.Name)
			continue
		}
		alias := d.Alias
		if alias == "" {
			alias = d.Name
		}
		info := &DictionaryInfo{ID: r.id, Title: d.Name, Alias: alias, Index: i, Styles: r.styles,
			FrequencyMode: r.frequencyMode, Secondary: d.AllowSecondarySearches, POSFilter: d.FilterPartsOfSpeech()}
		l.byTitle[d.Name] = info
		l.byID[r.id] = info
		ids = append(ids, strconv.FormatInt(r.id, 10))
	}
	if len(ids) == 0 {
		return nil, errors.New("none of the profile's enabled dictionaries have been imported")
	}
	l.idList = strings.Join(ids, ",")
	return l, nil
}

// Missing lists enabled dictionaries that have not been imported.
func (l *Lookup) Missing() []string { return l.missing }

// Dictionary returns information about an enabled dictionary.
func (l *Lookup) Dictionary(title string) *DictionaryInfo { return l.byTitle[title] }

// RuleNames returns user facing names for inflection rule ids.
func (l *Lookup) RuleNames(rules []string) []string {
	if l.deinflect == nil {
		return rules
	}
	return l.deinflect.RuleNames(rules)
}

// Find looks up exact dictionary forms without deinflection, trying each
// query in turn. It is the fallback when a word cannot be located in its
// sentence.
func (l *Lookup) Find(queries ...string) ([]*Entry, error) {
	var all []string
	for _, q := range queries {
		all = append(all, q, japanese.KatakanaToHiragana(q, true))
	}
	for _, q := range uniqueNonEmpty(all...) {
		terms, err := l.lookupTexts([]string{q})
		if err != nil {
			return nil, err
		}
		var entries []*Entry
		for _, t := range terms {
			src := Source{OriginalText: q, TransformedText: q, DeinflectedText: q, MatchSource: t.matchSource, IsPrimary: true}
			entries = append(entries, l.entryFromTerm(t, src, nil, nil))
		}
		if len(entries) > 0 {
			return l.finalize(entries)
		}
	}
	return nil, nil
}

func uniqueNonEmpty(items ...string) []string {
	var out []string
	for _, s := range items {
		if s != "" {
			out = appendUnique(out, s)
		}
	}
	return out
}

type dbTerm struct {
	id                                   int64
	dictID                               int64
	term, reading, defTags, rules, tTags string
	score, sequence                      int64
	glossary                             string
	matchSource                          string
	matched                              string // the looked up text that matched
}

func (l *Lookup) queryTerms(where string, args ...any) ([]dbTerm, error) {
	rows, err := l.st.DB.Query(`SELECT id, dict_id, expression, reading, definition_tags, rules, score, glossary, sequence, term_tags
		FROM terms WHERE dict_id IN (`+l.idList+`) AND `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dbTerm
	for rows.Next() {
		var t dbTerm
		if err := rows.Scan(&t.id, &t.dictID, &t.term, &t.reading, &t.defTags, &t.rules, &t.score,
			&t.glossary, &t.sequence, &t.tTags); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// lookupTexts finds terms whose expression or reading equals one of texts,
// like Yomitan's exact findTermsBulk. Results keep the order of texts.
func (l *Lookup) lookupTexts(texts []string) ([]dbTerm, error) {
	var out []dbTerm
	for _, text := range texts {
		byTerm, err := l.queryTerms(`expression = ?`, text)
		if err != nil {
			return nil, err
		}
		byReading, err := l.queryTerms(`reading = ? AND expression != ?`, text, text)
		if err != nil {
			return nil, err
		}
		for _, t := range byTerm {
			t.matchSource, t.matched = "term", text
			out = append(out, t)
		}
		for _, t := range byReading {
			t.matchSource, t.matched = "reading", text
			out = append(out, t)
		}
	}
	return out, nil
}

func (l *Lookup) conditionFlags(rules string) int {
	if v, ok := l.conditions[rules]; ok {
		return v
	}
	v := 0
	if l.deinflect != nil {
		v = l.deinflect.ConditionFlags(strings.Fields(rules))
	}
	l.conditions[rules] = v
	return v
}

type deinflection struct {
	originalText, transformedText, deinflectedText string
	conditions                                     int
	textChains                                     [][]string
	inflectionChains                               [][]string
}

// textVariants mirrors the Japanese text preprocessors that matter for book
// text: the original plus its hiragana and katakana conversions, each with
// its text processor chain.
func textVariants(text string) ([]string, [][][]string) {
	variants := []string{text}
	chains := [][][]string{{{}}}
	for _, v := range []struct{ text, id string }{
		{japanese.KatakanaToHiragana(text, true), "katakanaToHiragana"},
		{japanese.HiraganaToKatakana(text), "hiraganaToKatakana"},
	} {
		if !containsStr(variants, v.text) {
			variants = append(variants, v.text)
			chains = append(chains, [][]string{{v.id}})
		}
	}
	return variants, chains
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// FindAt returns the entries Yomitan would show when scanning sentence at
// rune offset start (Translator.findTerms with deinflection), sorted so the
// first entry is the one Yomitan would mine.
func (l *Lookup) FindAt(sentence string, start int) ([]*Entry, error) {
	entries, err := l.collect(sentence, start)
	if err != nil || len(entries) == 0 {
		return nil, err
	}
	return l.finalize(entries)
}

// collect builds the ungrouped entries for a scan at start, like
// Translator._findTermsInternal.
func (l *Lookup) collect(sentence string, start int) ([]*Entry, error) {
	runes := []rune(sentence)
	if start < 0 || start >= len(runes) {
		return nil, nil
	}
	text := runes[start:min(len(runes), start+l.opts.ScanLength)]

	var deinflections []deinflection
	for n := len(text); n > 0; n-- {
		raw := string(text[:n])
		variants, chains := textVariants(raw)
		for i, v := range variants {
			results := []deinflect.Result{{Text: v}}
			if l.deinflect != nil {
				var err error
				if results, err = l.deinflect.Transform(v); err != nil {
					return nil, err
				}
			}
			for _, r := range results {
				deinflections = append(deinflections, deinflection{
					originalText: raw, transformedText: v, deinflectedText: r.Text, conditions: r.Conditions,
					textChains: chains[i], inflectionChains: [][]string{r.Rules},
				})
			}
		}
	}

	var unique []string
	byText := map[string][]int{}
	for i, d := range deinflections {
		if _, ok := byText[d.deinflectedText]; !ok {
			unique = append(unique, d.deinflectedText)
		}
		byText[d.deinflectedText] = append(byText[d.deinflectedText], i)
	}
	terms, err := l.lookupTexts(unique)
	if err != nil {
		return nil, err
	}
	matches := make([][]dbTerm, len(deinflections))
	for _, t := range terms {
		filter := l.byID[t.dictID].POSFilter
		flags := l.conditionFlags(t.rules)
		for _, i := range byText[t.matched] {
			if !filter || deinflect.ConditionsMatch(deinflections[i].conditions, flags) {
				matches[i] = append(matches[i], t)
			}
		}
	}

	// Port of Translator._getDictionaryEntries.
	var entries []*Entry
	byID := map[int64]int{}
	for i, d := range deinflections {
		for _, t := range matches[i] {
			src := Source{OriginalText: d.originalText, TransformedText: d.transformedText, DeinflectedText: d.deinflectedText,
				MatchSource: t.matchSource, IsPrimary: true}
			idx, seen := byID[t.id]
			if !seen {
				byID[t.id] = len(entries)
				entries = append(entries, l.entryFromTerm(t, src, d.textChains, d.inflectionChains))
				continue
			}
			existing := entries[idx]
			existingTransformed := existing.Headwords[0].Sources[0].TransformedText
			newLen, oldLen := len([]rune(d.transformedText)), len([]rune(existingTransformed))
			switch {
			case newLen < oldLen:
			case newLen > oldLen:
				if d.originalText != existingTransformed {
					entries[idx] = l.entryFromTerm(t, src, d.textChains, d.inflectionChains)
				}
			default:
				existing.InflectionChains = appendUniqueChains(existing.InflectionChains, d.inflectionChains)
				existing.TextProcessorChains = appendUniqueChains(existing.TextProcessorChains, d.textChains)
			}
		}
	}
	return entries, nil
}

func chainKey(c []string) string { return strings.Join(c, "\x00") }

func chainsKey(chains [][]string) string {
	parts := make([]string, len(chains))
	for i, c := range chains {
		parts[i] = chainKey(c)
	}
	return strings.Join(parts, "\x01")
}

func appendUniqueChains(list, items [][]string) [][]string {
outer:
	for _, it := range items {
		for _, existing := range list {
			if chainKey(existing) == chainKey(it) {
				continue outer
			}
		}
		list = append(list, it)
	}
	return list
}

// finalize groups entries by the result mode, adds meta data and tags, and
// sorts everything like Translator.findTerms.
func (l *Lookup) finalize(entries []*Entry) ([]*Entry, error) {
	var err error
	switch l.opts.ResultOutputMode {
	case "group":
		entries = l.groupBy(entries, func(e *Entry) string {
			h := e.Headwords[0]
			return h.Term + "\x00" + japanese.KatakanaToHiragana(h.Reading, false) + "\x00" + chainsKey(e.InflectionChains)
		})
	case "term":
		entries = l.groupBy(entries, func(e *Entry) string { return e.Headwords[0].Term + "\x00" + chainsKey(e.InflectionChains) })
	case "merge":
		if entries, err = l.merge(entries); err != nil {
			return nil, err
		}
	}

	if err := l.addTermMeta(entries); err != nil {
		return nil, err
	}
	if err := l.expandTags(entries); err != nil {
		return nil, err
	}
	if l.opts.SortFrequencyDictionary != "" {
		updateSortFrequencies(entries, l.opts.SortFrequencyDictionary, l.opts.SortFrequencyDictionaryOrder == "ascending")
	}
	sortEntries(entries)
	for _, e := range entries {
		flagRedundantDefinitionTags(e.Definitions)
		sortDefinitions(e.Definitions)
		sort.SliceStable(e.Frequencies, func(i, j int) bool {
			return simpleLess(e.Frequencies[i].HeadwordIndex, e.Frequencies[i].DictionaryIndex, e.Frequencies[i].Index,
				e.Frequencies[j].HeadwordIndex, e.Frequencies[j].DictionaryIndex, e.Frequencies[j].Index)
		})
		sort.SliceStable(e.Pronunciations, func(i, j int) bool {
			return simpleLess(e.Pronunciations[i].HeadwordIndex, e.Pronunciations[i].DictionaryIndex, e.Pronunciations[i].Index,
				e.Pronunciations[j].HeadwordIndex, e.Pronunciations[j].DictionaryIndex, e.Pronunciations[j].Index)
		})
	}
	return entries, nil
}

func (l *Lookup) entryFromTerm(t dbTerm, src Source, textChains, inflectionChains [][]string) *Entry {
	info := l.byID[t.dictID]
	reading := t.reading
	if reading == "" {
		reading = t.term
	}
	exact := 0
	if src.IsPrimary && src.DeinflectedText == t.term {
		exact = 1
	}
	var entries []json.RawMessage
	json.Unmarshal([]byte(t.glossary), &entries)
	h := &Headword{
		Term:        t.term,
		Reading:     reading,
		Sources:     []Source{src},
		WordClasses: strings.Fields(t.rules),
	}
	h.tagGroups = addTags(nil, info.Title, strings.Fields(t.tTags))
	seq := t.sequence
	if seq < 0 {
		seq = -1
	}
	d := &Definition{
		HeadwordIndices: []int{0},
		Dictionary:      info.Title,
		DictionaryIndex: info.Index,
		DictionaryAlias: info.Alias,
		ID:              t.id,
		Score:           t.score,
		Sequences:       []int64{seq},
		IsPrimary:       src.IsPrimary,
		Entries:         entries,
	}
	d.tagGroups = addTags(nil, info.Title, strings.Fields(t.defTags))
	return &Entry{
		IsPrimary:                 src.IsPrimary,
		Score:                     t.score,
		DictionaryIndex:           info.Index,
		SourceTermExactMatchCount: exact,
		MaxOriginalTextLength:     len([]rune(src.OriginalText)),
		TextProcessorChains:       textChains,
		InflectionChains:          inflectionChains,
		Headwords:                 []*Headword{h},
		Definitions:               []*Definition{d},
	}
}

func (l *Lookup) groupBy(entries []*Entry, key func(*Entry) string) []*Entry {
	var order []string
	groups := map[string][]*Entry{}
	for _, e := range entries {
		k := key(e)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	out := make([]*Entry, 0, len(order))
	for _, k := range order {
		out = append(out, createGroupedEntry(groups[k], false))
	}
	return out
}

// merge implements Yomitan's "merge" result mode: entries from the main
// dictionary sharing a sequence number are combined with related entries.
func (l *Lookup) merge(entries []*Entry) ([]*Entry, error) {
	type group struct {
		ids     map[int64]bool
		entries []*Entry
	}
	var groups []*group
	bySeq := map[int64]*group{}
	var ungrouped []*Entry
	ungroupedIDs := map[int64]*Entry{}
	main := l.byTitle[l.opts.MainDictionary]
	for _, e := range entries {
		d := e.Definitions[0]
		if main != nil && d.Dictionary == main.Title && d.Sequences[0] >= 0 {
			g, ok := bySeq[d.Sequences[0]]
			if !ok {
				g = &group{ids: map[int64]bool{}}
				bySeq[d.Sequences[0]] = g
				groups = append(groups, g)
			}
			g.entries = append(g.entries, e)
			g.ids[d.ID] = true
		} else {
			ungrouped = append(ungrouped, e)
			ungroupedIDs[d.ID] = e
		}
	}
	if len(groups) > 0 {
		// Related entries from the main dictionary with the same sequence.
		for seq, g := range bySeq {
			related, err := l.queryTerms(`dict_id = ? AND sequence = ?`, main.ID, seq)
			if err != nil {
				return nil, err
			}
			for _, t := range related {
				if g.ids[t.id] {
					continue
				}
				t.matchSource = "term"
				g.entries = append(g.entries, l.entryFromTerm(t, Source{OriginalText: t.term, TransformedText: t.term, DeinflectedText: t.term, MatchSource: "term"}, nil, nil))
				g.ids[t.id] = true
				delete(ungroupedIDs, t.id)
			}
			sort.SliceStable(g.entries, func(i, j int) bool { return g.entries[i].Definitions[0].ID < g.entries[j].Definitions[0].ID })
		}
		// Secondary: entries from other dictionaries with the same headwords.
		for _, g := range groups {
			headwords := map[string]bool{}
			var terms []string
			for _, e := range g.entries {
				for _, h := range e.Headwords {
					headwords[h.Term+"\x00"+h.Reading] = true
					terms = appendUnique(terms, h.Term)
				}
			}
			for id, e := range ungroupedIDs {
				h := e.Headwords[0]
				if headwords[h.Term+"\x00"+h.Reading] {
					g.entries = append(g.entries, e)
					g.ids[id] = true
					delete(ungroupedIDs, id)
				}
			}
			var secondary []string
			for _, d := range l.byTitle {
				if d.Secondary {
					secondary = append(secondary, strconv.FormatInt(d.ID, 10))
				}
			}
			if len(secondary) == 0 {
				continue
			}
			for _, term := range terms {
				rows, err := l.queryTerms(`expression = ? AND dict_id IN (`+strings.Join(secondary, ",")+`)`, term)
				if err != nil {
					return nil, err
				}
				for _, t := range rows {
					reading := t.reading
					if reading == "" {
						reading = t.term
					}
					if g.ids[t.id] || !headwords[t.term+"\x00"+reading] {
						continue
					}
					t.matchSource = "term"
					g.entries = append(g.entries, l.entryFromTerm(t, Source{OriginalText: t.term, TransformedText: t.term, DeinflectedText: t.term, MatchSource: "term"}, nil, nil))
					g.ids[t.id] = true
				}
			}
		}
	}
	var out []*Entry
	for _, g := range groups {
		out = append(out, createGroupedEntry(g.entries, true))
	}
	var rest []*Entry
	for _, e := range ungrouped {
		if _, ok := ungroupedIDs[e.Definitions[0].ID]; ok {
			rest = append(rest, e)
		}
	}
	out = append(out, l.groupBy(rest, func(e *Entry) string {
		h := e.Headwords[0]
		return h.Term + "\x00" + japanese.KatakanaToHiragana(h.Reading, false)
	})...)
	return out, nil
}

func createGroupedEntry(entries []*Entry, checkDuplicates bool) *Entry {
	if len(entries) <= 1 {
		checkDuplicates = false
	}
	headwords := map[string]*Headword{}
	var headwordList []*Headword
	headwordDictIndex := map[int]int{}
	type defEntry struct {
		entry *Entry
		hwMap []int
	}
	var defEntries []defEntry
	for _, e := range entries {
		var hwMap []int
		for _, h := range e.Headwords {
			key := h.Term + "\x00" + japanese.KatakanaToHiragana(h.Reading, false)
			hw, ok := headwords[key]
			if !ok {
				hw = &Headword{Index: len(headwordList), Term: h.Term, Reading: h.Reading}
				headwords[key] = hw
				headwordList = append(headwordList, hw)
			}
			for _, s := range h.Sources {
				hw.Sources = appendUniqueSource(hw.Sources, s)
			}
			hw.WordClasses = appendUnique(hw.WordClasses, h.WordClasses...)
			hw.tagGroups = mergeTagGroups(hw.tagGroups, h.tagGroups)
			hwMap = append(hwMap, hw.Index)
		}
		for _, idx := range hwMap {
			if cur, ok := headwordDictIndex[idx]; !ok || e.DictionaryIndex < cur {
				headwordDictIndex[idx] = e.DictionaryIndex
			}
		}
		defEntries = append(defEntries, defEntry{e, hwMap})
	}

	g := &Entry{Score: math.MinInt64, DictionaryIndex: math.MaxInt32}
	defsByKey := map[string]*Definition{}
	hasInflections := false
	for _, de := range defEntries {
		e := de.entry
		if e.Score > g.Score {
			g.Score = e.Score
		}
		if e.DictionaryIndex < g.DictionaryIndex {
			g.DictionaryIndex = e.DictionaryIndex
		}
		if e.IsPrimary {
			g.IsPrimary = true
			if e.MaxOriginalTextLength > g.MaxOriginalTextLength {
				g.MaxOriginalTextLength = e.MaxOriginalTextLength
			}
			if !hasInflections || len(e.InflectionChains) < len(g.InflectionChains) {
				g.InflectionChains = e.InflectionChains
			}
			if !hasInflections || len(e.TextProcessorChains) < len(g.TextProcessorChains) {
				g.TextProcessorChains = e.TextProcessorChains
			}
			hasInflections = true
		}
		for _, d := range e.Definitions {
			var hwIdx []int
			for _, i := range d.HeadwordIndices {
				hwIdx = append(hwIdx, de.hwMap[i])
			}
			if !checkDuplicates {
				nd := *d
				nd.Index = len(g.Definitions)
				nd.HeadwordIndices = hwIdx
				g.Definitions = append(g.Definitions, &nd)
				continue
			}
			key := d.Dictionary
			for _, en := range d.Entries {
				key += "\x00" + string(en)
			}
			existing, ok := defsByKey[key]
			if !ok {
				nd := *d
				nd.Index = len(g.Definitions)
				nd.HeadwordIndices = nil
				nd.Sequences = append([]int64{}, d.Sequences...)
				nd.tagGroups = nil
				g.Definitions = append(g.Definitions, &nd)
				defsByKey[key] = &nd
				existing = &nd
			} else {
				if d.IsPrimary {
					existing.IsPrimary = true
				}
				existing.Sequences = appendUnique(existing.Sequences, d.Sequences...)
			}
			for _, i := range hwIdx {
				existing.HeadwordIndices = appendUnique(existing.HeadwordIndices, i)
			}
			sort.Ints(existing.HeadwordIndices)
			existing.tagGroups = mergeTagGroups(existing.tagGroups, d.tagGroups)
		}
	}

	// Primary headwords first, then by dictionary order.
	sort.SliceStable(headwordList, func(i, j int) bool {
		a, b := headwordList[i], headwordList[j]
		ap, bp := hasPrimary(a), hasPrimary(b)
		if ap != bp {
			return ap
		}
		return dictIndexOr(headwordDictIndex, a.Index) < dictIndexOr(headwordDictIndex, b.Index)
	})
	remap := map[int]int{}
	for i, h := range headwordList {
		remap[h.Index] = i
		h.Index = i
	}
	for _, d := range g.Definitions {
		for i, idx := range d.HeadwordIndices {
			if n, ok := remap[idx]; ok {
				d.HeadwordIndices[i] = n
			}
		}
	}
	g.Headwords = headwordList
	for _, h := range headwordList {
		for _, s := range h.Sources {
			if s.IsPrimary && s.MatchSource == "term" {
				g.SourceTermExactMatchCount++
				break
			}
		}
	}
	return g
}

func hasPrimary(h *Headword) bool {
	for _, s := range h.Sources {
		if s.IsPrimary {
			return true
		}
	}
	return false
}

func dictIndexOr(m map[int]int, i int) int {
	if v, ok := m[i]; ok {
		return v
	}
	return math.MaxInt32
}

func appendUniqueSource(list []Source, s Source) []Source {
	for i, e := range list {
		if e.OriginalText == s.OriginalText && e.TransformedText == s.TransformedText &&
			e.DeinflectedText == s.DeinflectedText && e.MatchSource == s.MatchSource {
			if s.IsPrimary {
				list[i].IsPrimary = true
			}
			return list
		}
	}
	return append(list, s)
}

// addTermMeta attaches frequencies, pitch accents and IPA transcriptions.
func (l *Lookup) addTermMeta(entries []*Entry) error {
	type target struct {
		entry         *Entry
		headwordIndex int
	}
	byTerm := map[string]map[string][]target{}
	var terms []any
	for _, e := range entries {
		for i, h := range e.Headwords {
			m, ok := byTerm[h.Term]
			if !ok {
				m = map[string][]target{}
				byTerm[h.Term] = m
				terms = append(terms, h.Term)
			}
			m[h.Reading] = append(m[h.Reading], target{e, i})
		}
	}
	if len(terms) == 0 {
		return nil
	}
	rows, err := l.st.DB.Query(`SELECT dict_id, expression, mode, data FROM term_meta
		WHERE dict_id IN (`+l.idList+`) AND expression IN (?`+strings.Repeat(",?", len(terms)-1)+`)
		ORDER BY rowid`, terms...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var dictID int64
		var term, mode, data string
		if err := rows.Scan(&dictID, &term, &mode, &data); err != nil {
			return err
		}
		info := l.byID[dictID]
		for reading, targets := range byTerm[term] {
			switch mode {
			case "freq":
				var withReading struct {
					Reading   *string         `json:"reading"`
					Frequency json.RawMessage `json:"frequency"`
				}
				raw := json.RawMessage(data)
				hasReading := json.Unmarshal(raw, &withReading) == nil && withReading.Reading != nil
				if hasReading {
					if *withReading.Reading != reading {
						continue
					}
					raw = withReading.Frequency
				}
				value, display := frequencyInfo(raw)
				for _, t := range targets {
					t.entry.Frequencies = append(t.entry.Frequencies, &Frequency{
						Index: len(t.entry.Frequencies), HeadwordIndex: t.headwordIndex,
						Dictionary: info.Title, DictionaryIndex: info.Index, DictionaryAlias: info.Alias,
						HasReading: hasReading, Frequency: value, DisplayValue: display, FrequencyMode: info.FrequencyMode,
					})
				}
			case "pitch":
				var p struct {
					Reading string `json:"reading"`
					Pitches []struct {
						Position json.RawMessage `json:"position"`
						Tags     []string        `json:"tags"`
						Nasal    json.RawMessage `json:"nasal"`
						Devoice  json.RawMessage `json:"devoice"`
					} `json:"pitches"`
				}
				if json.Unmarshal([]byte(data), &p) != nil || p.Reading != reading {
					continue
				}
				var items []PronunciationItem
				for _, pp := range p.Pitches {
					item := PronunciationItem{
						Type:             "pitch-accent",
						Positions:        parsePitch(pp.Position),
						NasalPositions:   numberArray(pp.Nasal),
						DevoicePositions: numberArray(pp.Devoice),
					}
					item.tagGroups = addTags(nil, info.Title, pp.Tags)
					items = append(items, item)
				}
				l.addPronunciation(targetsToPairs(targets, func(t target) (*Entry, int) { return t.entry, t.headwordIndex }), info, items)
			case "ipa":
				var p struct {
					Reading        string `json:"reading"`
					Transcriptions []struct {
						IPA  string   `json:"ipa"`
						Tags []string `json:"tags"`
					} `json:"transcriptions"`
				}
				if json.Unmarshal([]byte(data), &p) != nil || p.Reading != reading {
					continue
				}
				var items []PronunciationItem
				for _, tr := range p.Transcriptions {
					item := PronunciationItem{Type: "phonetic-transcription", IPA: tr.IPA}
					item.tagGroups = addTags(nil, info.Title, tr.Tags)
					items = append(items, item)
				}
				l.addPronunciation(targetsToPairs(targets, func(t target) (*Entry, int) { return t.entry, t.headwordIndex }), info, items)
			}
		}
	}
	return rows.Err()
}

type entryHeadword struct {
	entry *Entry
	index int
}

func targetsToPairs[T any](ts []T, f func(T) (*Entry, int)) []entryHeadword {
	out := make([]entryHeadword, len(ts))
	for i, t := range ts {
		out[i].entry, out[i].index = f(t)
	}
	return out
}

func (l *Lookup) addPronunciation(targets []entryHeadword, info *DictionaryInfo, items []PronunciationItem) {
	for _, t := range targets {
		copied := make([]PronunciationItem, len(items))
		for i := range items {
			copied[i] = items[i]
			copied[i].tagGroups = append([]tagGroup{}, items[i].tagGroups...)
		}
		t.entry.Pronunciations = append(t.entry.Pronunciations, &Pronunciation{
			Index: len(t.entry.Pronunciations), HeadwordIndex: t.index,
			Dictionary: info.Title, DictionaryIndex: info.Index, DictionaryAlias: info.Alias, Items: copied,
		})
	}
}

var numberPrefix = regexp.MustCompile(`[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?`)

// frequencyInfo mirrors Translator._getFrequencyInfo.
func frequencyInfo(raw json.RawMessage) (float64, *string) {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return n, nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		v := 0.0
		if m := numberPrefix.FindString(s); m != "" {
			v, _ = strconv.ParseFloat(m, 64)
		}
		return v, &s
	}
	var obj struct {
		Value        *float64 `json:"value"`
		DisplayValue *string  `json:"displayValue"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		v := 0.0
		if obj.Value != nil {
			v = *obj.Value
		}
		return v, obj.DisplayValue
	}
	return 0, nil
}

func parsePitch(raw json.RawMessage) japanese.Pitch {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return japanese.Pitch{Position: int(n)}
	}
	var s string
	json.Unmarshal(raw, &s)
	return japanese.Pitch{Pattern: s}
}

func numberArray(raw json.RawMessage) []int {
	var n float64
	if json.Unmarshal(raw, &n) == nil {
		return []int{int(n)}
	}
	var list []float64
	json.Unmarshal(raw, &list)
	out := make([]int, len(list))
	for i, v := range list {
		out[i] = int(v)
	}
	return out
}

// expandTags resolves tag names into tags using each dictionary's tag bank.
func (l *Lookup) expandTags(entries []*Entry) error {
	var err error
	expand := func(groups []tagGroup) []*Tag {
		var tags []*Tag
		for _, g := range groups {
			for _, name := range g.names {
				t, e := l.createTag(g.dictionary, name)
				if e != nil && err == nil {
					err = e
				}
				tags = append(tags, t)
			}
		}
		return groupTags(tags)
	}
	for _, e := range entries {
		for _, h := range e.Headwords {
			h.Tags = expand(h.tagGroups)
		}
		for _, d := range e.Definitions {
			d.Tags = expand(d.tagGroups)
		}
		for _, p := range e.Pronunciations {
			for i := range p.Items {
				p.Items[i].Tags = expand(p.Items[i].tagGroups)
			}
		}
	}
	return err
}

func (l *Lookup) createTag(dictionary, name string) (*Tag, error) {
	key := dictionary + "\x00" + name
	if cached, ok := l.tags[key]; ok {
		t := cached
		t.Content = append([]string{}, cached.Content...)
		t.Dictionaries = []string{dictionary}
		return &t, nil
	}
	t := &Tag{Name: name, Category: "default", Dictionaries: []string{dictionary}}
	defer func() { l.tags[key] = *t }()
	info := l.byTitle[dictionary]
	if info == nil {
		return t, nil
	}
	base := name
	if i := strings.Index(name, ":"); i >= 0 {
		base = name[:i]
	}
	var category, notes string
	var order, score float64
	err := l.st.DB.QueryRow(`SELECT category, ord, notes, score FROM tag_meta WHERE dict_id = ? AND name = ? LIMIT 1`,
		info.ID, base).Scan(&category, &order, &notes, &score)
	if errors.Is(err, sql.ErrNoRows) {
		return t, nil
	}
	if err != nil {
		return t, fmt.Errorf("tag %s: %w", name, err)
	}
	if category != "" {
		t.Category = category
	}
	t.Order, t.Score = order, score
	if notes != "" {
		t.Content = []string{notes}
	}
	return t, nil
}

func groupTags(tags []*Tag) []*Tag {
	if len(tags) <= 1 {
		return tags
	}
	var merged []*Tag
outer:
	for _, t := range tags {
		for _, m := range merged {
			if m.Name == t.Name && m.Category == t.Category {
				m.Order = math.Min(m.Order, t.Order)
				m.Score = math.Max(m.Score, t.Score)
				m.Dictionaries = append(m.Dictionaries, t.Dictionaries...)
				m.Content = appendUnique(m.Content, t.Content...)
				continue outer
			}
		}
		merged = append(merged, t)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Order != merged[j].Order {
			return merged[i].Order < merged[j].Order
		}
		return merged[i].Name < merged[j].Name
	})
	return merged
}

func flagRedundantDefinitionTags(defs []*Definition) {
	lastDictionary := ""
	lastPOS := ""
	first := true
	for _, d := range defs {
		var pos []string
		for _, t := range d.Tags {
			if t.Category == "partOfSpeech" {
				pos = append(pos, t.Name)
			}
		}
		sort.Strings(pos)
		key, _ := json.Marshal(pos)
		if first || lastDictionary != d.Dictionary {
			lastDictionary = d.Dictionary
			lastPOS = ""
			first = false
		}
		if lastPOS == string(key) {
			for _, t := range d.Tags {
				if t.Category == "partOfSpeech" {
					t.Redundant = true
				}
			}
		} else {
			lastPOS = string(key)
		}
	}
}

func updateSortFrequencies(entries []*Entry, dictionary string, ascending bool) {
	order := func(lo, hi float64, found bool) int64 {
		switch {
		case found && ascending:
			return int64(lo)
		case found:
			return -int64(hi)
		case ascending:
			return math.MaxInt64
		default:
			return 0
		}
	}
	for _, e := range entries {
		freqs := map[int]float64{}
		lo, hi, found := math.Inf(1), math.Inf(-1), false
		for _, f := range e.Frequencies {
			if f.Dictionary != dictionary {
				continue
			}
			freqs[f.HeadwordIndex] = f.Frequency
			lo, hi, found = math.Min(lo, f.Frequency), math.Max(hi, f.Frequency), true
		}
		e.FrequencyOrder = order(lo, hi, found)
		for _, d := range e.Definitions {
			lo, hi, found := math.Inf(1), math.Inf(-1), false
			for _, i := range d.HeadwordIndices {
				if v, ok := freqs[i]; ok {
					lo, hi, found = math.Min(lo, v), math.Max(hi, v), true
				}
			}
			d.FrequencyOrder = order(lo, hi, found)
		}
	}
}

func sortEntries(entries []*Entry) {
	sort.SliceStable(entries, func(a, b int) bool {
		v1, v2 := entries[a], entries[b]
		if v1.MaxOriginalTextLength != v2.MaxOriginalTextLength {
			return v1.MaxOriginalTextLength > v2.MaxOriginalTextLength
		}
		if c1, c2 := shortestChain(v1.TextProcessorChains), shortestChain(v2.TextProcessorChains); c1 != c2 {
			return c1 < c2
		}
		if c1, c2 := shortestChain(v1.InflectionChains), shortestChain(v2.InflectionChains); c1 != c2 {
			return c1 < c2
		}
		if v1.SourceTermExactMatchCount != v2.SourceTermExactMatchCount {
			return v1.SourceTermExactMatchCount > v2.SourceTermExactMatchCount
		}
		if v1.FrequencyOrder != v2.FrequencyOrder {
			return v1.FrequencyOrder < v2.FrequencyOrder
		}
		if v1.DictionaryIndex != v2.DictionaryIndex {
			return v1.DictionaryIndex < v2.DictionaryIndex
		}
		if v1.Score != v2.Score {
			return v1.Score > v2.Score
		}
		for j := 0; j < len(v1.Headwords) && j < len(v2.Headwords); j++ {
			t1, t2 := v1.Headwords[j].Term, v2.Headwords[j].Term
			if l1, l2 := len([]rune(t1)), len([]rune(t2)); l1 != l2 {
				return l1 > l2
			}
			if t1 != t2 {
				return t1 < t2
			}
		}
		return len(v1.Definitions) > len(v2.Definitions)
	})
}

// shortestChain is the length of the shortest candidate chain (0 if none).
func shortestChain(chains [][]string) int {
	if len(chains) == 0 {
		return 0
	}
	n := math.MaxInt32
	for _, c := range chains {
		n = min(n, len(c))
	}
	return n
}

func sortDefinitions(defs []*Definition) {
	sort.SliceStable(defs, func(a, b int) bool {
		v1, v2 := defs[a], defs[b]
		if v1.FrequencyOrder != v2.FrequencyOrder {
			return v1.FrequencyOrder < v2.FrequencyOrder
		}
		if v1.DictionaryIndex != v2.DictionaryIndex {
			return v1.DictionaryIndex < v2.DictionaryIndex
		}
		if v1.Score != v2.Score {
			return v1.Score > v2.Score
		}
		if len(v1.HeadwordIndices) != len(v2.HeadwordIndices) {
			return len(v1.HeadwordIndices) > len(v2.HeadwordIndices)
		}
		for j := range v1.HeadwordIndices {
			if v1.HeadwordIndices[j] != v2.HeadwordIndices[j] {
				return v1.HeadwordIndices[j] < v2.HeadwordIndices[j]
			}
		}
		return v1.Index < v2.Index
	})
}

func simpleLess(h1, d1, i1, h2, d2, i2 int) bool {
	if h1 != h2 {
		return h1 < h2
	}
	if d1 != d2 {
		return d1 < d2
	}
	return i1 < i2
}

// ParseText splits text into terms with furigana the way Yomitan's
// scanning parser does (Backend._textParseScanning): at each position the
// longest dictionary match is taken, and runs of unmatched characters are
// grouped together.
func (l *Lookup) ParseText(text string) ([][]japanese.Segment, error) {
	runes := []rune(text)
	var results [][]japanese.Segment
	ungrouped := -1
	for i := 0; i < len(runes); {
		ch := runes[i]
		entries, err := l.collect(text, i)
		if err != nil {
			return nil, err
		}
		length := 0
		for _, e := range entries {
			length = max(length, e.MaxOriginalTextLength)
		}
		if len(entries) > 0 && length > 0 && (length != 1 || japanese.IsCodePointJapanese(ch)) {
			sortEntries(entries)
			h := entries[0].Headwords[0]
			segments := japanese.DistributeFuriganaInflected(h.Term, h.Reading, string(runes[i:i+length]))
			if len(segments) > 0 {
				results = append(results, segments)
				ungrouped = -1
				i += length
				continue
			}
		}
		if ungrouped < 0 {
			results = append(results, []japanese.Segment{{Text: string(ch)}})
			ungrouped = len(results) - 1
		} else {
			results[ungrouped][0].Text += string(ch)
		}
		i++
	}
	return results, nil
}
