// Package sentencepage writes the original ann2html page: every Kindle
// lookup's sentence with the looked up word in bold, for mining by hand
// with Yomitan in a browser.
package sentencepage

import (
	_ "embed"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xythh/ann2html/internal/deinflect"
	"github.com/xythh/ann2html/internal/kindle"
)

// template is the original ann2html template.html. It ends inside <body>;
// the sentences and closing tags are appended.
//
//go:embed template.html
var template string

// FileName is the page written next to the executable, as in the original.
const FileName = "edit.html"

// scanLength bounds how far an inflected form may extend past the word's
// start, like Yomitan's default scan length.
const scanLength = 16

// Write writes the page for lookups (oldest first, so new lookups are added
// at the end and the page's bookmark keeps its place) to path.
func Write(path string, lookups []kindle.Lookup, d *deinflect.Deinflector) error {
	var b strings.Builder
	b.WriteString(template)
	for _, lk := range lookups {
		// Same output as the original fmt.Fprintln(f, "<p>", sentence, "</p>").
		b.WriteString("<p> " + Sentence(lk, d) + " </p>\n")
	}
	b.WriteString("</body>\n</html>\n")

	tmp, err := os.CreateTemp(filepath.Dir(path), ".edit-*.html")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Sentence returns the lookup's sentence as HTML with every occurrence of
// the word in <b>: the text at the lookup spot, the dictionary form, and
// any other inflected form of it in the sentence.
func Sentence(lk kindle.Lookup, d *deinflect.Deinflector) string {
	runes := []rune(lk.Usage)
	var words []string
	add := func(w string) {
		if w == "" {
			return
		}
		for _, x := range words {
			if x == w {
				return
			}
		}
		words = append(words, w)
	}
	start := lk.WordStart()
	if start >= 0 {
		add(wordAt(runes, start, lk, d, true))
	}
	add(lk.Lemma)
	if lemma := []rune(lk.Lemma); len(lemma) > 0 {
		for i, r := range runes {
			if r == lemma[0] && i != start {
				add(wordAt(runes, i, lk, d, false))
			}
		}
	}
	return boldOccurrences(lk.Usage, words)
}

// wordAt returns the longest text starting at start that is the looked up
// word or deinflects to it. At the lookup spot itself (atLookup), text that
// only shares a prefix with the dictionary form is accepted as a fallback.
func wordAt(runes []rune, start int, lk kindle.Lookup, d *deinflect.Deinflector, atLookup bool) string {
	text := runes[start:min(len(runes), start+scanLength)]
	for n := len(text); n > 0; n-- {
		candidate := string(text[:n])
		if candidate == lk.Lemma || (atLookup && candidate == lk.Surface) {
			return candidate
		}
		if d == nil {
			continue
		}
		results, err := d.Transform(candidate)
		if err != nil {
			continue
		}
		for _, r := range results {
			if len(r.Rules) > 0 && r.Text == lk.Lemma {
				return candidate
			}
		}
	}
	if !atLookup {
		return ""
	}
	// Not an inflection the rules know: bold the part shared with the
	// dictionary form.
	lemma := []rune(lk.Lemma)
	n := 0
	for n < len(text) && n < len(lemma) && text[n] == lemma[n] {
		n++
	}
	return string(text[:n])
}

// boldOccurrences HTML-escapes text and wraps every occurrence of words
// (longest first, left to right) in <b>.
func boldOccurrences(text string, words []string) string {
	sort.SliceStable(words, func(i, j int) bool { return len(words[i]) > len(words[j]) })
	var b strings.Builder
	for i := 0; i < len(text); {
		matched := ""
		for _, w := range words {
			if w != "" && strings.HasPrefix(text[i:], w) {
				matched = w
				break
			}
		}
		if matched != "" {
			b.WriteString("<b>" + html.EscapeString(matched) + "</b>")
			i += len(matched)
			continue
		}
		j := i + 1
		for j < len(text) && !startsAny(text[j:], words) {
			j++
		}
		b.WriteString(html.EscapeString(text[i:j]))
		i = j
	}
	return b.String()
}

func startsAny(s string, words []string) bool {
	for _, w := range words {
		if w != "" && strings.HasPrefix(s, w) {
			return true
		}
	}
	return false
}
