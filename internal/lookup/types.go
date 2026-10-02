// Package lookup finds dictionary entries for a word in the imported Yomitan
// dictionaries, building them the same way Yomitan's translator does
// (ext/js/language/translator.js) so cards render identically.
package lookup

import (
	"encoding/json"

	"github.com/xythh/ann2html/internal/japanese"
)

// Tag mirrors Yomitan's expanded dictionary tag.
type Tag struct {
	Name         string
	Category     string
	Order        float64
	Score        float64
	Content      []string
	Dictionaries []string
	Redundant    bool
}

// Notes returns the first note, as Yomitan's Anki note data does.
func (t *Tag) Notes() string {
	if len(t.Content) > 0 {
		return t.Content[0]
	}
	return ""
}

type Source struct {
	OriginalText    string
	TransformedText string
	DeinflectedText string
	MatchSource     string // "term" or "reading"
	IsPrimary       bool
}

type Headword struct {
	Index       int
	Term        string
	Reading     string
	Sources     []Source
	Tags        []*Tag
	WordClasses []string

	tagGroups []tagGroup
}

type Definition struct {
	Index           int
	HeadwordIndices []int
	Dictionary      string
	DictionaryIndex int
	DictionaryAlias string
	ID              int64
	Score           int64
	FrequencyOrder  int64
	Sequences       []int64
	IsPrimary       bool
	Tags            []*Tag
	Entries         []json.RawMessage

	tagGroups []tagGroup
}

type Frequency struct {
	Index           int
	HeadwordIndex   int
	Dictionary      string
	DictionaryIndex int
	DictionaryAlias string
	HasReading      bool
	Frequency       float64
	DisplayValue    *string
	FrequencyMode   string
}

// PronunciationItem is a pitch accent or a phonetic transcription.
type PronunciationItem struct {
	Type             string // "pitch-accent" or "phonetic-transcription"
	Positions        japanese.Pitch
	NasalPositions   []int
	DevoicePositions []int
	IPA              string
	Tags             []*Tag

	tagGroups []tagGroup
}

type Pronunciation struct {
	Index           int
	HeadwordIndex   int
	Dictionary      string
	DictionaryIndex int
	DictionaryAlias string
	Items           []PronunciationItem
}

// tagGroup holds unexpanded tag names from one dictionary.
type tagGroup struct {
	dictionary string
	names      []string
}

func addTags(groups []tagGroup, dictionary string, names []string) []tagGroup {
	if len(names) == 0 {
		return groups
	}
	for i := range groups {
		if groups[i].dictionary == dictionary {
			groups[i].names = appendUnique(groups[i].names, names...)
			return groups
		}
	}
	return append(groups, tagGroup{dictionary, appendUnique(nil, names...)})
}

func mergeTagGroups(dst, src []tagGroup) []tagGroup {
	for _, g := range src {
		dst = addTags(dst, g.dictionary, g.names)
	}
	return dst
}

func appendUnique[T comparable](list []T, items ...T) []T {
outer:
	for _, it := range items {
		for _, existing := range list {
			if existing == it {
				continue outer
			}
		}
		list = append(list, it)
	}
	return list
}

// Entry is a term dictionary entry, equivalent to Yomitan's
// TermDictionaryEntry.
type Entry struct {
	IsPrimary                 bool
	Score                     int64
	FrequencyOrder            int64
	DictionaryIndex           int
	SourceTermExactMatchCount int
	MaxOriginalTextLength     int
	Headwords                 []*Headword
	Definitions               []*Definition
	Pronunciations            []*Pronunciation
	Frequencies               []*Frequency
}

// PrimarySource returns the first primary source of any headword.
func (e *Entry) PrimarySource() *Source {
	for _, h := range e.Headwords {
		for i := range h.Sources {
			if h.Sources[i].IsPrimary {
				return &h.Sources[i]
			}
		}
	}
	return nil
}
