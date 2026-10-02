// Package japanese ports the Japanese text helpers from Yomitan
// (ext/js/language/ja/japanese.js) that card rendering depends on.
package japanese

import (
	"strings"
	"unicode/utf8"
)

type rng struct{ lo, hi rune }

var (
	hiraganaRange           = rng{0x3040, 0x309f}
	katakanaRange           = rng{0x30a0, 0x30ff}
	hiraganaConversionRange = rng{0x3041, 0x3096}
	katakanaConversionRange = rng{0x30a1, 0x30f6}

	cjkIdeographRanges = []rng{
		{0x4e00, 0x9fff}, {0x3400, 0x4dbf}, {0x20000, 0x2a6df}, {0x2a700, 0x2b73f},
		{0x2b740, 0x2b81f}, {0x2b820, 0x2ceaf}, {0x2ceb0, 0x2ebef}, {0x30000, 0x3134f},
		{0x31350, 0x323af}, {0x2ebf0, 0x2ee5f}, {0xf900, 0xfaff}, {0x2f800, 0x2fa1f},
	}
	japaneseRanges = append([]rng{
		hiraganaRange, katakanaRange,
		{0xff66, 0xff9f}, {0x30fb, 0x30fc}, {0xff61, 0xff65}, {0x3000, 0x303f},
		{0xff10, 0xff19}, {0xff21, 0xff3a}, {0xff41, 0xff5a}, {0xff01, 0xff0f},
		{0xff1a, 0xff1f}, {0xff3b, 0xff3f}, {0xff5b, 0xff60}, {0xffe0, 0xffee},
	}, cjkIdeographRanges...)
)

const (
	katakanaSmallKa        = 0x30f5
	katakanaSmallKe        = 0x30f6
	prolongedSoundMark     = 0x30fc
	smallKana              = "ぁぃぅぇぉゃゅょゎァィゥェォャュョヮ"
	diacriticMappingSource = "うゔ-かが-きぎ-くぐ-けげ-こご-さざ-しじ-すず-せぜ-そぞ-ただ-ちぢ-つづ-てで-とど-はばぱひびぴふぶぷへべぺほぼぽワヷ-ヰヸ-ウヴ-ヱヹ-ヲヺ-カガ-キギ-クグ-ケゲ-コゴ-サザ-シジ-スズ-セゼ-ソゾ-タダ-チヂ-ツヅ-テデ-トド-ハバパヒビピフブプヘベペホボポ"
)

var kanaToVowel = map[rune]string{}

// DiacriticInfo describes a kana with a (han)dakuten.
type DiacriticInfo struct {
	Character string
	Type      string // "dakuten" or "handakuten"
}

var diacriticMapping = map[string]DiacriticInfo{}

func init() {
	for vowel, chars := range map[string]string{
		"a": "ぁあかがさざただなはばぱまゃやらゎわヵァアカガサザタダナハバパマャヤラヮワヵヷ",
		"i": "ぃいきぎしじちぢにひびぴみりゐィイキギシジチヂニヒビピミリヰヸ",
		"u": "ぅうくぐすずっつづぬふぶぷむゅゆるゥウクグスズッツヅヌフブプムュユルヴ",
		"e": "ぇえけげせぜてでねへべぺめれゑヶェエケゲセゼテデネヘベペメレヱヶヹ",
		"o": "ぉおこごそぞとどのほぼぽもょよろをォオコゴソゾトドノホボポモョヨロヲヺ",
	} {
		for _, c := range chars {
			kanaToVowel[c] = vowel
		}
	}
	for _, c := range "のノ" {
		kanaToVowel[c] = ""
	}
	src := []rune(diacriticMappingSource)
	for i := 0; i+2 < len(src); i += 3 {
		base := string(src[i])
		diacriticMapping[string(src[i+1])] = DiacriticInfo{base, "dakuten"}
		if src[i+2] != '-' {
			diacriticMapping[string(src[i+2])] = DiacriticInfo{base, "handakuten"}
		}
	}
}

func inRange(c rune, r rng) bool { return c >= r.lo && c <= r.hi }

func inRanges(c rune, rs []rng) bool {
	for _, r := range rs {
		if inRange(c, r) {
			return true
		}
	}
	return false
}

func IsCodePointKana(c rune) bool {
	return inRange(c, hiraganaRange) || inRange(c, katakanaRange)
}

func IsCodePointKanji(c rune) bool { return inRanges(c, cjkIdeographRanges) }

// IsStringPartiallyJapanese reports whether s contains any Japanese character.
func IsStringPartiallyJapanese(s string) bool {
	for _, c := range s {
		if inRanges(c, japaneseRanges) {
			return true
		}
	}
	return false
}

func IsStringEntirelyKana(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !IsCodePointKana(c) {
			return false
		}
	}
	return true
}

func prolongedHiragana(prev rune) (rune, bool) {
	v, ok := kanaToVowel[prev]
	if !ok {
		return 0, false
	}
	switch v {
	case "a":
		return 'あ', true
	case "i":
		return 'い', true
	case "u", "o":
		return 'う', true
	case "e":
		return 'え', true
	}
	return 0, false
}

// KatakanaToHiragana converts katakana to hiragana. Prolonged sound marks are
// replaced by the matching vowel unless keepProlonged is set.
func KatakanaToHiragana(text string, keepProlonged bool) string {
	offset := hiraganaConversionRange.lo - katakanaConversionRange.lo
	out := make([]rune, 0, len(text))
	for _, c := range text {
		switch {
		case c == katakanaSmallKa || c == katakanaSmallKe:
		case c == prolongedSoundMark:
			if !keepProlonged && len(out) > 0 {
				if c2, ok := prolongedHiragana(out[len(out)-1]); ok {
					c = c2
				}
			}
		case inRange(c, katakanaConversionRange):
			c += offset
		}
		out = append(out, c)
	}
	return string(out)
}

func HiraganaToKatakana(text string) string {
	offset := katakanaConversionRange.lo - hiraganaConversionRange.lo
	var b strings.Builder
	for _, c := range text {
		if inRange(c, hiraganaConversionRange) {
			c += offset
		}
		b.WriteRune(c)
	}
	return b.String()
}

// KanaDiacriticInfo returns the base kana of a voiced kana, if any.
func KanaDiacriticInfo(char string) (DiacriticInfo, bool) {
	d, ok := diacriticMapping[char]
	return d, ok
}

// Segment is a piece of a term with its furigana (empty if none needed).
type Segment struct {
	Text    string
	Reading string
}

type furiganaGroup struct {
	isKana         bool
	text           []rune
	textNormalized []rune
}

func hasPrefix(s, prefix []rune) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := range prefix {
		if s[i] != prefix[i] {
			return false
		}
	}
	return true
}

func segmentize(reading, readingNorm []rune, groups []furiganaGroup, start int) []Segment {
	groupCount := len(groups) - start
	if groupCount <= 0 {
		if len(reading) == 0 {
			return []Segment{}
		}
		return nil
	}
	g := groups[start]
	textLen := len(g.text)
	if g.isKana {
		if g.textNormalized != nil && hasPrefix(readingNorm, g.textNormalized) {
			segs := segmentize(reading[textLen:], readingNorm[textLen:], groups, start+1)
			if segs != nil {
				var head []Segment
				if hasPrefix(reading, g.text) {
					head = []Segment{{string(g.text), ""}}
				} else {
					head = furiganaKanaSegments(g.text, reading)
				}
				return append(head, segs...)
			}
		}
		return nil
	}
	var result []Segment
	for i := len(reading); i >= textLen; i-- {
		segs := segmentize(reading[i:], readingNorm[i:], groups, start+1)
		if segs != nil {
			if result != nil {
				return nil // ambiguous
			}
			result = append([]Segment{{string(g.text), string(reading[:i])}}, segs...)
		}
		if groupCount == 1 {
			break
		}
	}
	return result
}

func furiganaKanaSegments(text, reading []rune) []Segment {
	at := func(s []rune, i int) rune {
		if i < len(s) {
			return s[i]
		}
		return -1
	}
	var out []Segment
	start := 0
	state := at(reading, 0) == at(text, 0)
	sub := func(s []rune, a, b int) string {
		if b > len(s) {
			b = len(s)
		}
		if a > b {
			return ""
		}
		return string(s[a:b])
	}
	for i := 1; i < len(text); i++ {
		newState := at(reading, i) == at(text, i)
		if state == newState {
			continue
		}
		r := ""
		if !state {
			r = sub(reading, start, i)
		}
		out = append(out, Segment{string(text[start:i]), r})
		state = newState
		start = i
	}
	r := ""
	if !state {
		r = sub(reading, start, len(text))
	}
	return append(out, Segment{string(text[start:]), r})
}

// DistributeFurigana splits term into segments with the reading of each
// non-kana part.
func DistributeFurigana(term, reading string) []Segment {
	if reading == term {
		return []Segment{{term, ""}}
	}
	var groups []furiganaGroup
	first := true
	var prevKana bool
	for _, c := range term {
		k := IsCodePointKana(c)
		if !first && k == prevKana {
			groups[len(groups)-1].text = append(groups[len(groups)-1].text, c)
			continue
		}
		groups = append(groups, furiganaGroup{isKana: k, text: []rune{c}})
		prevKana, first = k, false
	}
	for i := range groups {
		if groups[i].isKana {
			groups[i].textNormalized = []rune(KatakanaToHiragana(string(groups[i].text), false))
		}
	}
	readingRunes := []rune(reading)
	readingNorm := []rune(KatakanaToHiragana(reading, false))
	if len(readingNorm) != len(readingRunes) {
		readingNorm = readingRunes
	}
	if segs := segmentize(readingRunes, readingNorm, groups, 0); segs != nil {
		return segs
	}
	return []Segment{{term, reading}}
}

func stemLength(a, b []rune) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// DistributeFuriganaInflected distributes furigana over source, an inflected
// form of term.
func DistributeFuriganaInflected(term, reading, source string) []Segment {
	termNorm := []rune(KatakanaToHiragana(term, false))
	readingNorm := []rune(KatakanaToHiragana(reading, false))
	sourceRunes := []rune(source)
	sourceNorm := []rune(KatakanaToHiragana(source, false))

	mainText := []rune(term)
	readingRunes := []rune(reading)
	stem := stemLength(termNorm, sourceNorm)
	readingStem := stemLength(readingNorm, sourceNorm)
	if readingStem > 0 && readingStem >= stem {
		mainText = readingRunes
		stem = readingStem
		readingRunes = append(append([]rune{}, sourceRunes[:stem]...), readingRunes[min(stem, len(readingRunes)):]...)
	}

	var segments []Segment
	if stem > 0 {
		mainText = append(append([]rune{}, sourceRunes[:stem]...), mainText[min(stem, len(mainText)):]...)
		consumed := 0
		for _, seg := range DistributeFurigana(string(mainText), string(readingRunes)) {
			start := consumed
			consumed += utf8.RuneCountInString(seg.Text)
			if consumed < stem {
				segments = append(segments, seg)
			} else if consumed == stem {
				segments = append(segments, seg)
				break
			} else {
				if start < stem {
					segments = append(segments, Segment{string(mainText[start:stem]), ""})
				}
				break
			}
		}
	}
	if stem < len(sourceRunes) {
		remainder := string(sourceRunes[stem:])
		if n := len(segments); n > 0 && segments[n-1].Reading == "" {
			segments[n-1].Text += remainder
		} else {
			segments = append(segments, Segment{remainder, ""})
		}
	}
	return segments
}

// GetKanaMorae splits kana text into morae, attaching small kana to the
// preceding character.
func GetKanaMorae(text string) []string {
	var morae []string
	for _, c := range text {
		if strings.ContainsRune(smallKana, c) && len(morae) > 0 {
			morae[len(morae)-1] += string(c)
		} else {
			morae = append(morae, string(c))
		}
	}
	return morae
}

func GetKanaMoraCount(text string) int { return len(GetKanaMorae(text)) }

// Pitch is a pitch accent value: a downstep position, or an explicit
// high/low pattern such as "LHHL".
type Pitch struct {
	Position int
	Pattern  string
}

func (p Pitch) IsPattern() bool { return p.Pattern != "" }

func IsMoraPitchHigh(i int, p Pitch) bool {
	if p.IsPattern() {
		return i < len(p.Pattern) && p.Pattern[i] == 'H'
	}
	switch p.Position {
	case 0:
		return i > 0
	case 1:
		return i < 1
	default:
		return i > 0 && i < p.Position
	}
}

func GetDownstepPositions(pattern string) []int {
	var out []int
	for i := 1; i < len(pattern); i++ {
		if pattern[i-1] == 'H' && pattern[i] == 'L' {
			out = append(out, i)
		}
	}
	if len(out) == 0 {
		if strings.HasPrefix(pattern, "L") {
			out = append(out, 0)
		} else {
			out = append(out, -1)
		}
	}
	return out
}

// GetPitchCategory returns heiban, atamadaka, nakadaka, odaka or kifuku, or
// "" when unknown.
func GetPitchCategory(text string, p Pitch, isVerbOrAdjective bool) string {
	pos := p.Position
	if p.IsPattern() {
		pos = GetDownstepPositions(p.Pattern)[0]
	}
	if pos == 0 {
		return "heiban"
	}
	if isVerbOrAdjective {
		if pos > 0 {
			return "kifuku"
		}
		return ""
	}
	if pos == 1 {
		return "atamadaka"
	}
	if pos > 1 {
		if pos >= GetKanaMoraCount(text) {
			return "odaka"
		}
		return "nakadaka"
	}
	return ""
}

// IsCodePointJapanese reports whether c is a Japanese character (kana,
// kanji, Japanese punctuation or full width characters).
func IsCodePointJapanese(c rune) bool { return inRanges(c, japaneseRanges) }
