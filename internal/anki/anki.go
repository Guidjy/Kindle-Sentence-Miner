// Package anki is a minimal AnkiConnect client.
package anki

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	URL    string
	APIKey string
	HTTP   *http.Client
}

func New(url, apiKey string) *Client {
	return &Client{URL: url, APIKey: apiKey, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// ErrUnsupportedAction is returned when AnkiConnect is too old for an action.
var ErrUnsupportedAction = errors.New("unsupported action")

func (c *Client) invoke(ctx context.Context, action string, params any, result any) error {
	body := map[string]any{"action": action, "version": 6}
	if params != nil {
		body["params"] = params
	}
	if c.APIKey != "" {
		body["key"] = c.APIKey
	}
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach AnkiConnect at %s (is Anki running with the AnkiConnect add-on?): %w", c.URL, err)
	}
	defer resp.Body.Close()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *string         `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fmt.Errorf("AnkiConnect %s: invalid response: %w", action, err)
	}
	if envelope.Error != nil {
		if strings.Contains(*envelope.Error, "unsupported action") {
			return fmt.Errorf("AnkiConnect %s: %w", action, ErrUnsupportedAction)
		}
		return fmt.Errorf("AnkiConnect %s: %s", action, *envelope.Error)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}

// Version checks connectivity and returns the AnkiConnect API version.
func (c *Client) Version(ctx context.Context) (int, error) {
	var v int
	err := c.invoke(ctx, "version", nil, &v)
	return v, err
}

func (c *Client) ModelFieldNames(ctx context.Context, model string) ([]string, error) {
	var names []string
	err := c.invoke(ctx, "modelFieldNames", map[string]any{"modelName": model}, &names)
	return names, err
}

type DuplicateScopeOptions struct {
	DeckName       *string `json:"deckName"`
	CheckChildren  bool    `json:"checkChildren"`
	CheckAllModels bool    `json:"checkAllModels"`
}

type NoteOptions struct {
	AllowDuplicate        bool                  `json:"allowDuplicate"`
	DuplicateScope        string                `json:"duplicateScope"`
	DuplicateScopeOptions DuplicateScopeOptions `json:"duplicateScopeOptions"`
}

type Note struct {
	DeckName  string            `json:"deckName"`
	ModelName string            `json:"modelName"`
	Fields    map[string]string `json:"fields"`
	Tags      []string          `json:"tags"`
	Options   NoteOptions       `json:"options"`
}

const duplicateError = "cannot create note because it is a duplicate"

// FindDuplicates reports which notes Anki would reject as duplicates, the
// way Yomitan's partitionAddibleNotes does. Notes should only contain their
// first field.
func (c *Client) FindDuplicates(ctx context.Context, notes []Note) ([]bool, error) {
	noDup := make([]Note, len(notes))
	for i, n := range notes {
		n.Options.AllowDuplicate = false
		noDup[i] = n
	}
	var detailed []struct {
		CanAdd bool    `json:"canAdd"`
		Error  *string `json:"error"`
	}
	err := c.invoke(ctx, "canAddNotesWithErrorDetail", map[string]any{"notes": noDup}, &detailed)
	if err == nil {
		out := make([]bool, len(notes))
		for i, d := range detailed {
			out[i] = d.Error != nil && strings.Contains(*d.Error, duplicateError)
		}
		return out, nil
	}
	if !errors.Is(err, ErrUnsupportedAction) {
		return nil, err
	}
	// Older AnkiConnect: compare canAddNotes with and without duplicates allowed.
	withDup := make([]Note, len(notes))
	for i, n := range notes {
		n.Options.AllowDuplicate = true
		withDup[i] = n
	}
	var allowed, strict []bool
	if err := c.invoke(ctx, "canAddNotes", map[string]any{"notes": withDup}, &allowed); err != nil {
		return nil, err
	}
	if err := c.invoke(ctx, "canAddNotes", map[string]any{"notes": noDup}, &strict); err != nil {
		return nil, err
	}
	out := make([]bool, len(notes))
	for i := range out {
		out[i] = allowed[i] != strict[i]
	}
	return out, nil
}

// AddNote adds a note and returns its id.
func (c *Client) AddNote(ctx context.Context, n Note) (int64, error) {
	var id *int64
	if err := c.invoke(ctx, "addNote", map[string]any{"note": n}, &id); err != nil {
		return 0, err
	}
	if id == nil {
		return 0, errors.New("AnkiConnect addNote: note was not added")
	}
	return *id, nil
}

// StoreMediaFile stores data in Anki's media folder and returns the file
// name Anki used.
func (c *Client) StoreMediaFile(ctx context.Context, filename string, data []byte) (string, error) {
	var name string
	err := c.invoke(ctx, "storeMediaFile", map[string]any{
		"filename": filename,
		"data":     base64.StdEncoding.EncodeToString(data),
	}, &name)
	if err == nil && name == "" {
		name = filename
	}
	return name, err
}

// RootDeckName returns the top level deck of a deck name ("A::B" -> "A").
func RootDeckName(deck string) string {
	if i := strings.Index(deck, "::"); i >= 0 {
		return deck[:i]
	}
	return deck
}
