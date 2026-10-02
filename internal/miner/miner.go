// Package miner turns new Kindle lookups into Anki notes.
package miner

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/xythh/ann2html/internal/anki"
	"github.com/xythh/ann2html/internal/audio"
	"github.com/xythh/ann2html/internal/deinflect"
	"github.com/xythh/ann2html/internal/japanese"
	"github.com/xythh/ann2html/internal/kindle"
	"github.com/xythh/ann2html/internal/lookup"
	"github.com/xythh/ann2html/internal/render"
	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

type Config struct {
	Store     *store.Store
	VocabPath string
	RescanAll bool // ignore the saved timestamp; duplicates are still skipped
	Settings  *settings.Export
	Profile   string
	Workers   int
}

// Status is a snapshot of a mining run.
type Status struct {
	Phase        string
	Total        int // lookups read from vocab.db
	Processed    int
	NoDefinition int
	Skipped      int // multi-word selections, left for mining by hand
	Duplicates   int // already in Anki, or the same word earlier in this run
	Added        int
	Failed       int
	Log          []string
}

type run struct {
	cfg      Config
	format   *settings.CardFormat
	profile  *settings.Profile
	client   *anki.Client
	lookup   *lookup.Lookup
	audio    *audio.Downloader
	renderer render.Options
	// highlightOpen/Close are the tags the card format wraps around
	// {cloze-body}; they also highlight the word in {sentence-furigana}.
	highlightOpen, highlightClose string
	scanLength                    int

	mu       sync.Mutex
	status   Status
	report   func(Status)
	media    map[string]string // dictionary + "\x00" + path -> Anki file name ("" = unavailable)
	sounds   map[string]string // term + "\x00" + reading -> Anki file name ("" = none)
	inflight map[string]*sync.WaitGroup

	// lookupMu guards lookup, which workers use to parse sentences.
	lookupMu sync.Mutex
	furigana map[string][][]japanese.Segment
}

func (r *run) update(fn func(s *Status)) {
	r.mu.Lock()
	fn(&r.status)
	s := r.status
	s.Log = append([]string(nil), r.status.Log...)
	r.mu.Unlock()
	if r.report != nil {
		r.report(s)
	}
}

func (r *run) logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.update(func(s *Status) { s.Log = append(s.Log, msg) })
}

// Run mines new lookups. report is called with a status snapshot after every
// change and may be nil.
func Run(ctx context.Context, cfg Config, report func(Status)) (Status, error) {
	r := &run{cfg: cfg, report: report, media: map[string]string{}, sounds: map[string]string{}, inflight: map[string]*sync.WaitGroup{}, furigana: map[string][][]japanese.Segment{}}
	err := r.run(ctx)
	if err != nil {
		r.logf("Error: %v", err)
	}
	r.update(func(s *Status) {
		if err != nil {
			s.Phase = "Failed"
		} else {
			s.Phase = "Done"
		}
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, err
}

type candidate struct {
	lk        kindle.Lookup
	entry     *lookup.Entry
	note      *render.Note
	firstVal  string
	duplicate bool
}

func (r *run) run(ctx context.Context) error {
	cfg := r.cfg
	if cfg.Settings == nil {
		return errors.New("import your Yomitan settings first")
	}
	r.profile = cfg.Settings.Profile(cfg.Profile)
	opts := r.profile.Options
	format, err := opts.Anki.TermFormat()
	if err != nil {
		return err
	}
	if len(format.Fields) == 0 || format.Deck == "" || format.Model == "" {
		return errors.New("the Yomitan profile's term card format has no deck, note type or fields")
	}
	r.format = format
	if opts.Anki.HasCustomTemplates() {
		r.logf("Warning: your Yomitan profile has customized Anki field templates; the default templates are used instead")
	}
	if b := opts.Anki.DuplicateBehavior; b != "" && b != "prevent" {
		r.logf("Note: duplicate behavior %q is treated as \"prevent\"", b)
	}

	r.update(func(s *Status) { s.Phase = "Connecting to Anki" })
	r.client = anki.New(opts.Anki.ServerURL(), opts.Anki.APIKey)
	if _, err := r.client.Version(ctx); err != nil {
		return err
	}
	fieldNames, err := r.client.ModelFieldNames(ctx, format.Model)
	if err != nil {
		return fmt.Errorf("note type %q: %w", format.Model, err)
	}
	if len(fieldNames) == 0 {
		return fmt.Errorf("note type %q was not found in Anki", format.Model)
	}

	r.update(func(s *Status) { s.Phase = "Reading vocab.db" })
	// Only lookups in the profile's language can be mined: the dictionaries
	// and deinflection rules are for that language.
	language := opts.General.Language
	if language == "" {
		language = "ja"
	}
	var since int64
	if !cfg.RescanAll {
		if since, err = cfg.Store.LastTimestamp(); err != nil {
			return err
		}
	}
	lookups, err := kindle.ReadLookups(cfg.VocabPath, since, []string{language})
	if err != nil {
		return err
	}
	r.update(func(s *Status) { s.Total = len(lookups) })
	if len(lookups) == 0 {
		r.logf("No new lookups in vocab.db")
		return nil
	}

	deinflector, err := deinflect.New()
	if err != nil {
		return err
	}
	r.scanLength = opts.Scanning.ScanLength()
	r.lookup, err = lookup.New(cfg.Store, lookup.OptionsFromProfile(r.profile), deinflector)
	if err != nil {
		return err
	}
	if missing := r.lookup.Missing(); len(missing) > 0 {
		r.logf("Warning: enabled dictionaries not imported: %s", strings.Join(missing, ", "))
	}
	r.audio = audio.New(opts.Audio, opts.General.Language)
	var templates []string
	for _, f := range format.Fields {
		templates = append(templates, f.Value)
	}
	r.highlightOpen, r.highlightClose = render.ClozeWrapper(templates...)
	r.renderer = render.Options{
		ResultOutputMode:   lookup.OptionsFromProfile(r.profile).ResultOutputMode,
		GlossaryLayoutMode: opts.General.GlossaryLayoutMode,
		CompactTags:        opts.General.CompactTags,
		DictionaryStyles:   map[string]string{},
	}
	for _, d := range opts.Dictionaries.Enabled() {
		if info := r.lookup.Dictionary(d.Name); info != nil {
			r.renderer.Dictionaries = append(r.renderer.Dictionaries, d.Name)
			r.renderer.DictionaryStyles[d.Name] = info.Styles
		}
	}

	// maxHandled is the newest lookup dealt with; failedAt the oldest one that
	// must be retried next run.
	var tsMu sync.Mutex
	var maxHandled int64
	var failedAt int64 = -1
	handled := func(ts int64) {
		tsMu.Lock()
		maxHandled = max(maxHandled, ts)
		tsMu.Unlock()
	}
	failed := func(ts int64) {
		tsMu.Lock()
		if failedAt < 0 || ts < failedAt {
			failedAt = ts
		}
		tsMu.Unlock()
	}

	// 1. Look up every word and render the first field (the duplicate key).
	r.update(func(s *Status) { s.Phase = "Looking up words" })
	var cands []*candidate
	seen := map[string]bool{}
	for _, lk := range lookups {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, start, err := r.findEntries(lk)
		if err != nil {
			return err
		}
		if isMultiWordSelection(lk, entries, start) {
			handled(lk.Timestamp)
			r.logf("Skipped multi-word selection %q; mine it by hand from the sentences page", lk.Lemma)
			r.update(func(s *Status) { s.Skipped++; s.Processed++ })
			continue
		}
		if len(entries) == 0 {
			handled(lk.Timestamp)
			r.logf("No definition: %s", lk.Lemma)
			r.update(func(s *Status) { s.NoDefinition++; s.Processed++ })
			continue
		}
		c := &candidate{lk: lk, entry: entries[0]}
		c.note = r.newNote(lk, c.entry, start)
		c.firstVal = c.note.Field(format.Fields[0].Value)
		if seen[c.firstVal] {
			handled(lk.Timestamp)
			r.update(func(s *Status) { s.Duplicates++; s.Processed++ })
			continue
		}
		seen[c.firstVal] = true
		cands = append(cands, c)
	}

	// 2. Ask Anki which notes already exist, like Yomitan does.
	if opts.Anki.DuplicateCheck() && len(cands) > 0 {
		r.update(func(s *Status) { s.Phase = "Checking for duplicates in Anki" })
		for start := 0; start < len(cands); start += 100 {
			end := min(start+100, len(cands))
			probes := make([]anki.Note, 0, end-start)
			for _, c := range cands[start:end] {
				probes = append(probes, r.ankiNote(map[string]string{format.Fields[0].Name: c.firstVal}))
			}
			dups, err := r.client.FindDuplicates(ctx, probes)
			if err != nil {
				return err
			}
			for i, d := range dups {
				cands[start+i].duplicate = d
			}
		}
	}

	// 3. Render the remaining notes (downloading audio and images) and add them.
	r.update(func(s *Status) { s.Phase = "Adding notes" })
	workers := cfg.Workers
	if workers <= 0 {
		workers = 4
	}
	jobs := make(chan *candidate)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				if r.addNote(ctx, c) {
					handled(c.lk.Timestamp)
				} else {
					failed(c.lk.Timestamp)
				}
			}
		}()
	}
	for _, c := range cands {
		if ctx.Err() != nil {
			break
		}
		if c.duplicate {
			handled(c.lk.Timestamp)
			r.update(func(s *Status) { s.Duplicates++; s.Processed++ })
			continue
		}
		select {
		case jobs <- c:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()

	if err := ctx.Err(); err != nil {
		// Cancelled: keep the saved position so nothing is skipped.
		return err
	}
	// Advance the saved position. If anything failed, stop just before the
	// earliest failure so it is retried next run; lookups after it will then
	// be skipped as duplicates.
	cur, err := cfg.Store.LastTimestamp()
	if err != nil {
		return err
	}
	target := maxHandled
	if failedAt >= 0 {
		target = failedAt - 1
	}
	if failedAt >= 0 || target > cur {
		return cfg.Store.SetLastTimestamp(target)
	}
	return nil
}

func (r *run) ankiNote(fields map[string]string) anki.Note {
	a := r.profile.Options.Anki
	scope := a.DuplicateScope
	if scope == "" {
		scope = "collection"
	}
	n := anki.Note{
		DeckName:  r.format.Deck,
		ModelName: r.format.Model,
		Fields:    fields,
		Tags:      append([]string{}, a.Tags...),
		Options: anki.NoteOptions{
			AllowDuplicate: false,
			DuplicateScope: scope,
			DuplicateScopeOptions: anki.DuplicateScopeOptions{
				CheckChildren:  false,
				CheckAllModels: a.DuplicateScopeCheckAllModels,
			},
		},
	}
	if scope == "deck-root" {
		root := anki.RootDeckName(r.format.Deck)
		n.Options.DuplicateScope = "deck"
		n.Options.DuplicateScopeOptions.DeckName = &root
		n.Options.DuplicateScopeOptions.CheckChildren = true
	}
	if n.Tags == nil {
		n.Tags = []string{}
	}
	return n
}

func (r *run) addNote(ctx context.Context, c *candidate) bool {
	fields := map[string]string{}
	for _, f := range r.format.Fields {
		fields[f.Name] = c.note.Field(f.Value)
	}
	note := r.ankiNote(fields)
	if !r.profile.Options.Anki.DuplicateCheck() {
		note.Options.AllowDuplicate = true
	}
	if _, err := r.client.AddNote(ctx, note); err != nil {
		if strings.Contains(err.Error(), "duplicate") {
			r.update(func(s *Status) { s.Duplicates++; s.Processed++ })
			return true
		}
		r.logf("Failed to add %s: %v", c.lk.Lemma, err)
		r.update(func(s *Status) { s.Failed++; s.Processed++ })
		return false
	}
	r.logf("Added %s", c.entry.Headwords[0].Term)
	r.update(func(s *Status) { s.Added++; s.Processed++ })
	return true
}

// isMultiWordSelection reports whether the user selected several words on
// the Kindle. The Kindle stores a manual selection as the word, and its first
// token as the stem. Yomitan's top match at the start of the selection is the
// first word; the selection spans several words when the rest of it contains
// another content word (kanji, katakana or letters), as in は、潔く or
// として雇う. Selections of one word plus trailing grammar (参ったな, 淡々と)
// or of part of a word (咎めなかっ) are mined. Multi-word selections are
// skipped because it is unclear which word should become a card.
func isMultiWordSelection(lk kindle.Lookup, entries []*lookup.Entry, start int) bool {
	if !lk.MaybeSelection() || len(entries) == 0 || start < 0 {
		return false
	}
	if utf8.RuneCountInString(lk.Usage[:strings.Index(lk.Usage, lk.Lemma)]) != start {
		return false
	}
	src := entries[0].PrimarySource()
	if src == nil {
		return false
	}
	rest := []rune(lk.Lemma)[min(utf8.RuneCountInString(src.OriginalText), utf8.RuneCountInString(lk.Lemma)):]
	for _, c := range rest {
		if isContentRune(c) {
			return true
		}
	}
	return false
}

// isContentRune reports whether c can only belong to a content word: kanji,
// katakana (except the prolonged sound mark) or letters and digits.
func isContentRune(c rune) bool {
	switch {
	case japanese.IsCodePointKanji(c):
		return true
	case c >= 0x30a1 && c <= 0x30fa:
		return true
	case c >= 0x3040 && c <= 0x30ff, c >= 0x3000 && c <= 0x303f:
		return false
	}
	return unicode.IsLetter(c) || unicode.IsDigit(c)
}

// findEntries returns Yomitan's entries for the word at its position in the
// sentence, falling back to an exact lookup of the Kindle's forms. start is
// -1 when the word could not be located.
func (r *run) findEntries(lk kindle.Lookup) ([]*lookup.Entry, int, error) {
	if start := lk.WordStart(); start >= 0 {
		entries, err := r.lookup.FindAt(lk.Usage, start)
		if err != nil || len(entries) > 0 {
			return entries, start, err
		}
	}
	entries, err := r.lookup.Find(lk.Lemma, lk.Surface)
	return entries, -1, err
}

func (r *run) newNote(lk kindle.Lookup, e *lookup.Entry, start int) *render.Note {
	sentence := []rune(lk.Usage)
	offset, original := len(sentence), ""
	if start >= 0 {
		offset = start
		if src := e.PrimarySource(); src != nil {
			original = src.OriginalText
		}
	}
	query := string(sentence[min(offset, len(sentence)):min(len(sentence), offset+r.scanLength)])
	if start < 0 {
		query = lk.Surface
	}
	term, reading := noteHeadword(e)
	n := &render.Note{
		Entry: e,
		Context: render.Context{
			Sentence:       lk.Usage,
			SentenceOffset: offset,
			OriginalText:   original,
			DocumentTitle:  lk.BookTitle,
			FullQuery:      query,
		},
		Options:   r.renderer,
		Media:     r.dictionaryMedia,
		Audio:     func() (string, bool) { return r.termAudio(term, reading) },
		RuleNames: r.lookup.RuleNames,
	}
	n.SentenceFurigana = func(plain bool) (string, bool) {
		terms, ok := r.parseSentence(lk.Usage)
		if !ok {
			return "", false
		}
		// Highlight the word like the card's sentence field does.
		h := render.Highlight{Words: n.HighlightWords(), Open: r.highlightOpen, Close: r.highlightClose}
		if plain {
			return render.FuriganaPlain(terms, term, reading, h), true
		}
		return render.FuriganaHTML(terms, term, reading, h), true
	}
	return n
}

// parseSentence splits a sentence into terms with furigana, once per sentence.
func (r *run) parseSentence(sentence string) ([][]japanese.Segment, bool) {
	r.lookupMu.Lock()
	defer r.lookupMu.Unlock()
	if terms, ok := r.furigana[sentence]; ok {
		return terms, terms != nil
	}
	terms, err := r.lookup.ParseText(sentence)
	if err != nil {
		r.logf("Could not add furigana to a sentence: %v", err)
		terms = nil
	}
	r.furigana[sentence] = terms
	return terms, terms != nil
}

// noteHeadword picks the headword whose audio Yomitan would attach
// (AnkiNoteBuilder.getDictionaryEntryDetailsForNote).
func noteHeadword(e *lookup.Entry) (string, string) {
	best := -1
outer:
	for i, h := range e.Headwords {
		for _, s := range h.Sources {
			if h.Term == s.DeinflectedText {
				best = i
				break outer
			} else if h.Reading == s.DeinflectedText && best < 0 {
				best = i
				break
			}
		}
	}
	h := e.Headwords[max(best, 0)]
	return h.Term, h.Reading
}

// once runs fn for key a single time across workers and returns the cached result.
func (r *run) once(cache map[string]string, key string, fn func() string) string {
	r.mu.Lock()
	if v, ok := cache[key]; ok {
		r.mu.Unlock()
		return v
	}
	if wg, ok := r.inflight[key]; ok {
		r.mu.Unlock()
		wg.Wait()
		r.mu.Lock()
		v := cache[key]
		r.mu.Unlock()
		return v
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	r.inflight[key] = wg
	r.mu.Unlock()

	v := fn()

	r.mu.Lock()
	cache[key] = v
	delete(r.inflight, key)
	r.mu.Unlock()
	wg.Done()
	return v
}

func (r *run) termAudio(term, reading string) (string, bool) {
	name := r.once(r.sounds, "audio\x00"+term+"\x00"+reading, func() string {
		a, err := r.audio.Download(context.Background(), term, reading)
		if err != nil {
			r.logf("No audio for %s", term)
			return ""
		}
		stored, err := r.client.StoreMediaFile(context.Background(), a.FileName, a.Data)
		if err != nil {
			r.logf("Could not store audio for %s: %v", term, err)
			return ""
		}
		return stored
	})
	return name, name != ""
}

var imageExtensions = map[string]string{
	"image/apng": ".apng", "image/avif": ".avif", "image/bmp": ".bmp", "image/gif": ".gif",
	"image/x-icon": ".ico", "image/jpeg": ".jpeg", "image/png": ".png", "image/svg+xml": ".svg",
	"image/tiff": ".tiff", "image/webp": ".webp",
}

func (r *run) dictionaryMedia(dictionary, path string) (string, bool) {
	name := r.once(r.media, dictionary+"\x00"+path, func() string {
		info := r.lookup.Dictionary(dictionary)
		if info == nil {
			return ""
		}
		content, mediaType, err := r.cfg.Store.Media(info.ID, path)
		if err != nil || len(content) == 0 {
			return ""
		}
		sum := sha1.Sum(content)
		fileName := audio.MediaFileName("yomitan_dictionary_media_", hex.EncodeToString(sum[:]), imageExtensions[mediaType])
		stored, err := r.client.StoreMediaFile(context.Background(), fileName, content)
		if err != nil {
			r.logf("Could not store image %s: %v", path, err)
			return ""
		}
		return stored
	})
	return name, name != ""
}
