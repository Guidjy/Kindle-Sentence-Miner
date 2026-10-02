// Package store holds the application's SQLite database: persisted settings
// and the imported Yomitan dictionaries.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS settings(
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS dictionaries(
	id INTEGER PRIMARY KEY,
	title TEXT NOT NULL UNIQUE,
	revision TEXT NOT NULL DEFAULT '',
	version INTEGER NOT NULL DEFAULT 3,
	sequenced INTEGER NOT NULL DEFAULT 0,
	styles TEXT NOT NULL DEFAULT '',
	frequency_mode TEXT NOT NULL DEFAULT '',
	imported_at INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS terms(
	id INTEGER PRIMARY KEY,
	dict_id INTEGER NOT NULL,
	expression TEXT NOT NULL,
	reading TEXT NOT NULL,
	definition_tags TEXT NOT NULL DEFAULT '',
	rules TEXT NOT NULL DEFAULT '',
	score INTEGER NOT NULL DEFAULT 0,
	glossary TEXT NOT NULL,
	sequence INTEGER NOT NULL DEFAULT -1,
	term_tags TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS term_meta(
	dict_id INTEGER NOT NULL,
	expression TEXT NOT NULL,
	mode TEXT NOT NULL,
	data TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tag_meta(
	dict_id INTEGER NOT NULL,
	name TEXT NOT NULL,
	category TEXT NOT NULL DEFAULT '',
	ord INTEGER NOT NULL DEFAULT 0,
	notes TEXT NOT NULL DEFAULT '',
	score INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS media(
	dict_id INTEGER NOT NULL,
	path TEXT NOT NULL,
	media_type TEXT NOT NULL DEFAULT '',
	width INTEGER NOT NULL DEFAULT 0,
	height INTEGER NOT NULL DEFAULT 0,
	content BLOB,
	PRIMARY KEY(dict_id, path)
);
`

// Indexes are created after bulk imports, which is much faster than
// maintaining them during millions of inserts.
const indexes = `
CREATE INDEX IF NOT EXISTS terms_expr ON terms(expression);
CREATE INDEX IF NOT EXISTS terms_read ON terms(reading);
CREATE INDEX IF NOT EXISTS terms_dict ON terms(dict_id);
CREATE INDEX IF NOT EXISTS term_meta_expr ON term_meta(expression);
CREATE INDEX IF NOT EXISTS term_meta_dict ON term_meta(dict_id);
CREATE INDEX IF NOT EXISTS tag_meta_name ON tag_meta(dict_id, name);
`

// Settings keys.
const (
	KeyVocabPath      = "vocab_path"
	KeyLastTimestamp  = "last_timestamp"
	KeyActiveProfile  = "active_profile"
	KeyYomitanOptions = "yomitan_settings_json"
)

type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the application database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// modernc sqlite is safest with a single writer connection.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	if _, err := db.Exec(indexes); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating indexes: %w", err)
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

// DropIndexes removes lookup indexes before a bulk import.
func (s *Store) DropIndexes() error {
	_, err := s.DB.Exec(`DROP INDEX IF EXISTS terms_expr; DROP INDEX IF EXISTS terms_read;
		DROP INDEX IF EXISTS term_meta_expr; DROP INDEX IF EXISTS tag_meta_name;`)
	return err
}

// CreateIndexes (re)creates lookup indexes after a bulk import.
func (s *Store) CreateIndexes() error {
	_, err := s.DB.Exec(indexes)
	return err
}

func (s *Store) Get(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) Set(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings(key, value) VALUES(?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *Store) LastTimestamp() (int64, error) {
	v, err := s.Get(KeyLastTimestamp)
	if err != nil || v == "" {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

func (s *Store) SetLastTimestamp(ts int64) error {
	return s.Set(KeyLastTimestamp, strconv.FormatInt(ts, 10))
}

// DictionaryInfo summarizes an imported dictionary.
type DictionaryInfo struct {
	ID       int64
	Title    string
	Revision string
	Styles   string
	Terms    int64
	Meta     int64 // frequency, pitch and IPA entries
}

// Dictionaries lists imported dictionaries with their term counts.
func (s *Store) Dictionaries() ([]DictionaryInfo, error) {
	rows, err := s.DB.Query(`SELECT d.id, d.title, d.revision, d.styles,
		(SELECT COUNT(*) FROM terms t WHERE t.dict_id = d.id),
		(SELECT COUNT(*) FROM term_meta m WHERE m.dict_id = d.id)
		FROM dictionaries d ORDER BY d.title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DictionaryInfo
	for rows.Next() {
		var d DictionaryInfo
		if err := rows.Scan(&d.ID, &d.Title, &d.Revision, &d.Styles, &d.Terms, &d.Meta); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeleteDictionary removes a dictionary and all of its rows.
func DeleteDictionary(tx *sql.Tx, id int64) error {
	for _, table := range []string{"terms", "term_meta", "tag_meta", "media"} {
		if _, err := tx.Exec(`DELETE FROM `+table+` WHERE dict_id = ?`, id); err != nil {
			return err
		}
	}
	_, err := tx.Exec(`DELETE FROM dictionaries WHERE id = ?`, id)
	return err
}

// Media returns the stored file for a dictionary path.
func (s *Store) Media(dictID int64, path string) (content []byte, mediaType string, err error) {
	err = s.DB.QueryRow(`SELECT content, media_type FROM media WHERE dict_id = ? AND path = ?`,
		dictID, path).Scan(&content, &mediaType)
	return content, mediaType, err
}

// Incomplete reports whether dictionary data from an older version of the
// collection importer is present: rows of outbound-key tables were stored
// under an empty dictionary title. The collection must be re-imported.
func (s *Store) Incomplete() (bool, error) {
	var bad bool
	err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM dictionaries WHERE title = '')`).Scan(&bad)
	return bad, err
}
