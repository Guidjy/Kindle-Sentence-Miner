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

	"golang.org/x/text/language"
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

// ParseLanguages splits a comma separated list of ISO 639 codes. An empty
// string means all languages.
func ParseLanguages(s string) ([]string, error) {
	var out []string
	for _, v := range strings.Split(s, ",") {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, err := language.ParseBase(v); err != nil {
			return nil, fmt.Errorf("%q is not a valid ISO 639 language code", v)
		}
		out = append(out, v)
	}
	return out, nil
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
