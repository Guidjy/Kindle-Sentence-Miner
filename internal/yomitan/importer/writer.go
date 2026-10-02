// Package importer loads Yomitan dictionaries into the application database,
// either from a Yomitan "dictionary collection" export (Dexie JSON) or from
// individual dictionary .zip archives.
package importer

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/xythh/ann2html/internal/store"
)

// Progress reports import progress. total is 0 when unknown.
type Progress func(msg string, done, total int64)

const batchSize = 20000

// writer inserts rows in large transactions and maps dictionary titles to ids.
type writer struct {
	st      *store.Store
	tx      *sql.Tx
	pending int
	dictIDs map[string]int64

	term, meta, tag, media *sql.Stmt
}

func newWriter(st *store.Store) (*writer, error) {
	w := &writer{st: st, dictIDs: map[string]int64{}}
	var err error
	prep := func(q string) *sql.Stmt {
		if err != nil {
			return nil
		}
		var s *sql.Stmt
		s, err = st.DB.Prepare(q)
		return s
	}
	w.term = prep(`INSERT INTO terms(dict_id, expression, reading, definition_tags, rules, score, glossary, sequence, term_tags)
		VALUES(?,?,?,?,?,?,?,?,?)`)
	w.meta = prep(`INSERT INTO term_meta(dict_id, expression, mode, data) VALUES(?,?,?,?)`)
	w.tag = prep(`INSERT INTO tag_meta(dict_id, name, category, ord, notes, score) VALUES(?,?,?,?,?,?)`)
	w.media = prep(`INSERT OR REPLACE INTO media(dict_id, path, media_type, width, height, content) VALUES(?,?,?,?,?,?)`)
	if err != nil {
		w.closeStmts()
		return nil, err
	}
	if err := st.DropIndexes(); err != nil {
		w.closeStmts()
		return nil, err
	}
	if w.tx, err = st.DB.Begin(); err != nil {
		w.closeStmts()
		return nil, err
	}
	return w, nil
}

func (w *writer) closeStmts() {
	for _, s := range []*sql.Stmt{w.term, w.meta, w.tag, w.media} {
		if s != nil {
			s.Close()
		}
	}
}

// tick counts a written row and commits once a batch is full.
func (w *writer) tick() error {
	w.pending++
	if w.pending < batchSize {
		return nil
	}
	if err := w.tx.Commit(); err != nil {
		return err
	}
	w.pending = 0
	var err error
	w.tx, err = w.st.DB.Begin()
	return err
}

// finish commits the last batch and rebuilds indexes. On error it rolls back
// the current batch; rows from earlier batches stay, and re-importing the same
// dictionary replaces them.
func (w *writer) finish(err error) error {
	defer w.closeStmts()
	if err != nil {
		w.tx.Rollback()
		w.st.CreateIndexes()
		return err
	}
	if err := w.tx.Commit(); err != nil {
		return err
	}
	return w.st.CreateIndexes()
}

// dictID returns the id for title, replacing any previously imported
// dictionary with the same title the first time it is seen in this import.
func (w *writer) dictID(title string) (int64, error) {
	if id, ok := w.dictIDs[title]; ok {
		return id, nil
	}
	var old int64
	err := w.tx.QueryRow(`SELECT id FROM dictionaries WHERE title = ?`, title).Scan(&old)
	switch {
	case err == nil:
		if err := store.DeleteDictionary(w.tx, old); err != nil {
			return 0, err
		}
	case err != sql.ErrNoRows:
		return 0, err
	}
	res, err := w.tx.Exec(`INSERT INTO dictionaries(title, imported_at) VALUES(?, ?)`, title, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	w.dictIDs[title] = id
	return id, nil
}

type dictSummary struct {
	Title     string `json:"title"`
	Revision  string `json:"revision"`
	Sequenced bool   `json:"sequenced"`
	Version   int    `json:"version"`
	Format    int    `json:"format"`
	Styles    string `json:"styles"`
	// FrequencyMode is "rank-based" or "occurrence-based" for frequency dictionaries.
	FrequencyMode string `json:"frequencyMode"`
}

func (w *writer) addDictionary(d dictSummary) error {
	id, err := w.dictID(d.Title)
	if err != nil {
		return err
	}
	version := d.Version
	if version == 0 {
		version = d.Format
	}
	_, err = w.tx.Exec(`UPDATE dictionaries SET revision = ?, version = ?, sequenced = ?,
		styles = CASE WHEN ? = '' THEN styles ELSE ? END, frequency_mode = ? WHERE id = ?`,
		d.Revision, version, d.Sequenced, d.Styles, d.Styles, d.FrequencyMode, id)
	return err
}

func (w *writer) setStyles(title, styles string) error {
	id, err := w.dictID(title)
	if err != nil {
		return err
	}
	_, err = w.tx.Exec(`UPDATE dictionaries SET styles = ? WHERE id = ?`, styles, id)
	return err
}

type termRow struct {
	Dictionary     string          `json:"dictionary"`
	Expression     string          `json:"expression"`
	Reading        string          `json:"reading"`
	DefinitionTags *string         `json:"definitionTags"`
	Tags           *string         `json:"tags"`
	Rules          string          `json:"rules"`
	Score          float64         `json:"score"`
	Glossary       json.RawMessage `json:"glossary"`
	Sequence       *float64        `json:"sequence"`
	TermTags       string          `json:"termTags"`
}

func (w *writer) addTerm(t termRow) error {
	id, err := w.dictID(t.Dictionary)
	if err != nil {
		return err
	}
	reading := t.Reading
	if reading == "" {
		reading = t.Expression
	}
	defTags := ""
	if t.DefinitionTags != nil {
		defTags = *t.DefinitionTags
	} else if t.Tags != nil {
		defTags = *t.Tags
	}
	seq := int64(-1)
	if t.Sequence != nil {
		seq = int64(*t.Sequence)
	}
	glossary := string(t.Glossary)
	if glossary == "" {
		glossary = "[]"
	}
	if _, err := w.tx.Stmt(w.term).Exec(id, t.Expression, reading, strings.TrimSpace(defTags),
		strings.TrimSpace(t.Rules), int64(t.Score), glossary, seq, strings.TrimSpace(t.TermTags)); err != nil {
		return err
	}
	return w.tick()
}

type termMetaRow struct {
	Dictionary string          `json:"dictionary"`
	Expression string          `json:"expression"`
	Mode       string          `json:"mode"`
	Data       json.RawMessage `json:"data"`
}

func (w *writer) addTermMeta(m termMetaRow) error {
	id, err := w.dictID(m.Dictionary)
	if err != nil {
		return err
	}
	if _, err := w.tx.Stmt(w.meta).Exec(id, m.Expression, m.Mode, string(m.Data)); err != nil {
		return err
	}
	return w.tick()
}

type tagRow struct {
	Dictionary string  `json:"dictionary"`
	Name       string  `json:"name"`
	Category   string  `json:"category"`
	Order      float64 `json:"order"`
	Notes      string  `json:"notes"`
	Score      float64 `json:"score"`
}

func (w *writer) addTag(t tagRow) error {
	id, err := w.dictID(t.Dictionary)
	if err != nil {
		return err
	}
	if _, err := w.tx.Stmt(w.tag).Exec(id, t.Name, t.Category, int64(t.Order), t.Notes, int64(t.Score)); err != nil {
		return err
	}
	return w.tick()
}

type mediaRow struct {
	Dictionary string  `json:"dictionary"`
	Path       string  `json:"path"`
	MediaType  string  `json:"mediaType"`
	Width      float64 `json:"width"`
	Height     float64 `json:"height"`
	Content    []byte  `json:"-"`
}

func (w *writer) addMedia(m mediaRow) error {
	id, err := w.dictID(m.Dictionary)
	if err != nil {
		return err
	}
	if _, err := w.tx.Stmt(w.media).Exec(id, m.Path, m.MediaType, int64(m.Width), int64(m.Height), m.Content); err != nil {
		return err
	}
	return w.tick()
}

// clearAll removes every imported dictionary.
func (w *writer) clearAll() error {
	for _, table := range []string{"terms", "term_meta", "tag_meta", "media", "dictionaries"} {
		if _, err := w.tx.Exec(`DELETE FROM ` + table); err != nil {
			return err
		}
	}
	return nil
}
