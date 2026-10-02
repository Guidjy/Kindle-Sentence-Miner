// Package settings parses a Yomitan settings export
// (Settings → Backup → Export Settings).
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Export is the top level of a Yomitan settings export file.
type Export struct {
	Version int     `json:"version"`
	Options Options `json:"options"`
}

type Options struct {
	ProfileCurrent int       `json:"profileCurrent"`
	Profiles       []Profile `json:"profiles"`
}

type Profile struct {
	Name    string         `json:"name"`
	Options ProfileOptions `json:"options"`
}

type ProfileOptions struct {
	General      General      `json:"general"`
	Dictionaries Dictionaries `json:"dictionaries"`
	Anki         Anki         `json:"anki"`
	Audio        Audio        `json:"audio"`
}

type General struct {
	Language                     string `json:"language"`
	CompactTags                  bool   `json:"compactTags"`
	ResultOutputMode             string `json:"resultOutputMode"`
	MainDictionary               string `json:"mainDictionary"`
	SortFrequencyDictionary      string `json:"sortFrequencyDictionary"`
	SortFrequencyDictionaryOrder string `json:"sortFrequencyDictionaryOrder"`
	GlossaryLayoutMode           string `json:"glossaryLayoutMode"`
}

type Dictionary struct {
	Name                   string `json:"name"`
	Alias                  string `json:"alias"`
	Enabled                bool   `json:"enabled"`
	Priority               int    `json:"priority"`
	AllowSecondarySearches bool   `json:"allowSecondarySearches"`
}

// DisplayName is the alias if set, otherwise the dictionary title.
func (d Dictionary) DisplayName() string {
	if d.Alias != "" {
		return d.Alias
	}
	return d.Name
}

// Dictionaries is the profile's dictionary list in Yomitan's order.
// Current Yomitan versions store an array whose order is the priority
// order; older versions stored a map keyed by title with numeric
// priorities (higher first). Both are accepted.
type Dictionaries []Dictionary

func (d *Dictionaries) UnmarshalJSON(b []byte) error {
	var list []Dictionary
	if err := json.Unmarshal(b, &list); err == nil {
		*d = list
		return nil
	}
	var m map[string]Dictionary
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	list = list[:0]
	for name, v := range m {
		v.Name = name
		list = append(list, v)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Priority != list[j].Priority {
			return list[i].Priority > list[j].Priority
		}
		return list[i].Name < list[j].Name
	})
	*d = list
	return nil
}

// Enabled returns the enabled dictionaries in priority order.
func (d Dictionaries) Enabled() []Dictionary {
	var out []Dictionary
	for _, v := range d {
		if v.Enabled {
			out = append(out, v)
		}
	}
	return out
}

type Anki struct {
	Enable                       bool         `json:"enable"`
	Server                       string       `json:"server"`
	APIKey                       string       `json:"apiKey"`
	Tags                         []string     `json:"tags"`
	CardFormats                  []CardFormat `json:"cardFormats"`
	Terms                        *CardFormat  `json:"terms"` // pre-cardFormats exports
	CheckForDuplicates           *bool        `json:"checkForDuplicates"`
	DuplicateScope               string       `json:"duplicateScope"`
	DuplicateScopeCheckAllModels bool         `json:"duplicateScopeCheckAllModels"`
	DuplicateBehavior            string       `json:"duplicateBehavior"`
	FieldTemplates               *string      `json:"fieldTemplates"`
}

type CardFormat struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Deck   string `json:"deck"`
	Model  string `json:"model"`
	Fields Fields `json:"fields"`
}

// Field is one note field and its marker template, e.g. "{glossary}".
type Field struct {
	Name          string
	Value         string
	OverwriteMode string
}

// Fields keeps note fields in the order they appear in the export, which
// matches the note type's field order.
type Fields []Field

func (f *Fields) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("anki fields: expected object")
	}
	var out Fields
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		name, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return err
		}
		field := Field{Name: name}
		// Older exports store the marker template directly as a string.
		if err := json.Unmarshal(raw, &field.Value); err != nil {
			var obj struct {
				Value         string `json:"value"`
				OverwriteMode string `json:"overwriteMode"`
			}
			if err := json.Unmarshal(raw, &obj); err != nil {
				return fmt.Errorf("anki field %q: %w", name, err)
			}
			field.Value, field.OverwriteMode = obj.Value, obj.OverwriteMode
		}
		out = append(out, field)
	}
	*f = out
	return nil
}

type Audio struct {
	Enabled                   bool          `json:"enabled"`
	Sources                   []AudioSource `json:"sources"`
	EnableDefaultAudioSources bool          `json:"enableDefaultAudioSources"`
}

type AudioSource struct {
	Type  string `json:"type"`
	URL   string `json:"url"`
	Voice string `json:"voice"`
}

// Parse decodes a settings export.
func Parse(data []byte) (*Export, error) {
	var e Export
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("not a Yomitan settings export: %w", err)
	}
	if len(e.Options.Profiles) == 0 {
		return nil, errors.New("not a Yomitan settings export: no profiles found")
	}
	return &e, nil
}

// ProfileNames lists the profiles in export order.
func (e *Export) ProfileNames() []string {
	names := make([]string, len(e.Options.Profiles))
	for i, p := range e.Options.Profiles {
		names[i] = p.Name
	}
	return names
}

// Profile returns the profile called name, or the export's current profile
// when name is empty or unknown.
func (e *Export) Profile(name string) *Profile {
	for i := range e.Options.Profiles {
		if e.Options.Profiles[i].Name == name {
			return &e.Options.Profiles[i]
		}
	}
	i := e.Options.ProfileCurrent
	if i < 0 || i >= len(e.Options.Profiles) {
		i = 0
	}
	return &e.Options.Profiles[i]
}

// TermFormat returns the card format used for term notes.
func (a *Anki) TermFormat() (*CardFormat, error) {
	for i := range a.CardFormats {
		if a.CardFormats[i].Type == "term" || a.CardFormats[i].Type == "" {
			return &a.CardFormats[i], nil
		}
	}
	if a.Terms != nil && a.Terms.Model != "" {
		return a.Terms, nil
	}
	return nil, errors.New("the Yomitan profile has no term card format configured")
}

// ServerURL returns the AnkiConnect URL, defaulting like Yomitan.
func (a *Anki) ServerURL() string {
	if a.Server == "" {
		return "http://127.0.0.1:8765"
	}
	return a.Server
}

// DuplicateCheck reports whether duplicates should be checked (default true).
func (a *Anki) DuplicateCheck() bool {
	return a.CheckForDuplicates == nil || *a.CheckForDuplicates
}

// HasCustomTemplates reports whether the user edited Yomitan's handlebars
// field templates; those edits are not reproduced by this program.
func (a *Anki) HasCustomTemplates() bool {
	return a.FieldTemplates != nil && strings.TrimSpace(*a.FieldTemplates) != ""
}
