// Package kindle reads lookups from a Kindle Vocabulary Builder database.
package kindle

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/xythh/ann2html/internal/japanese"

	_ "modernc.org/sqlite"
)

// Lookup is one word lookup made on the Kindle.
type Lookup struct {
	ID        string
	Timestamp int64  // unix milliseconds
	Lemma     string // dictionary form chosen by the Kindle (WORDS.word)
	Surface   string // the word as tokenized in the book (WORDS.stem); often inflected
	Lang      string
	Usage     string // the sentence the word was looked up in
	BookTitle string
}

// ReadLookups returns lookups newer than since (unix ms), oldest first,
// restricted to langs when it is non-empty.
func ReadLookups(dbPath string, since int64, langs []string) ([]Lookup, error) {
	if info, err := os.Stat(dbPath); err != nil || info.IsDir() {
		return nil, errors.New("vocab.db was not found")
	}
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: "mode=ro"}
	if !strings.HasPrefix(u.Path, "/") {
		u.Path = "/" + u.Path
	}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()

	q := `SELECT l.id, l.timestamp, COALESCE(w.word, ''), COALESCE(w.stem, ''), COALESCE(w.lang, ''),
		COALESCE(l.usage, ''), COALESCE(b.title, '')
		FROM LOOKUPS l
		JOIN WORDS w ON w.id = l.word_key
		LEFT JOIN BOOK_INFO b ON b.id = l.book_key
		WHERE l.timestamp > ?`
	args := []any{since}
	if len(langs) > 0 {
		q += ` AND w.lang IN (?` + strings.Repeat(",?", len(langs)-1) + `)`
		for _, l := range langs {
			args = append(args, l)
		}
	}
	q += ` ORDER BY l.timestamp`

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("reading vocab.db: %w", err)
	}
	defer rows.Close()
	var out []Lookup
	for rows.Next() {
		var l Lookup
		if err := rows.Scan(&l.ID, &l.Timestamp, &l.Lemma, &l.Surface, &l.Lang, &l.Usage, &l.BookTitle); err != nil {
			return nil, err
		}
		if l.Surface == "" {
			l.Surface = l.Lemma
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// MaybeSelection reports whether the lookup has the shape of a multi-word
// selection: the Kindle stores the selected text as the word (so it appears
// verbatim in the sentence) and only its first token as the stem.
func (lk Lookup) MaybeSelection() bool {
	return lk.Lemma != lk.Surface && lk.Surface != "" &&
		strings.HasPrefix(lk.Lemma, lk.Surface) && strings.Contains(lk.Usage, lk.Lemma)
}

// WordStart finds where the looked up word appears in its sentence (rune
// offset), or -1. The Kindle's word (the dictionary form, or the whole
// selection when several words were selected) pins the exact spot when it
// appears verbatim; otherwise the surface form, else the longest prefix of
// the dictionary form (inflected words share their stem with it).
func (lk Lookup) WordStart() int {
	runeIndex := func(s string) int {
		if s == "" {
			return -1
		}
		if i := strings.Index(lk.Usage, s); i >= 0 {
			return utf8.RuneCountInString(lk.Usage[:i])
		}
		return -1
	}
	for _, s := range []string{lk.Lemma, lk.Surface} {
		if i := runeIndex(s); i >= 0 {
			return i
		}
	}
	lemma := []rune(lk.Lemma)
	for n := len(lemma) - 1; n >= 1; n-- {
		if n == 1 && !japanese.IsCodePointKanji(lemma[0]) {
			break
		}
		if i := runeIndex(string(lemma[:n])); i >= 0 {
			return i
		}
	}
	return -1
}
