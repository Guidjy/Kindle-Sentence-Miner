package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xythh/ann2html/internal/kindle"
	"github.com/xythh/ann2html/internal/miner"
	"github.com/xythh/ann2html/internal/store"
	"github.com/xythh/ann2html/internal/yomitan/importer"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

// App holds the application state shared between the GUI thread and the
// background tasks. All fields are guarded by mu.
type App struct {
	mu sync.Mutex
	st *store.Store

	vocabPath string
	export    *settings.Export
	profile   string
	languages string
	rescanAll bool
	dicts     []store.DictionaryInfo

	busy         string // description of the running task, "" when idle
	progress     float32
	progressText string
	status       miner.Status
	minerLogSeen int
	log          []string
	cancel       context.CancelFunc
}

// Snapshot is a copy of the state for drawing one frame.
type Snapshot struct {
	VocabPath    string
	Profiles     []string
	ProfileIndex int
	Deck, Model  string
	Warnings     []string
	Languages    string
	RescanAll    bool
	Dicts        []string
	Busy         string
	Progress     float32
	ProgressText string
	Status       miner.Status
	Log          []string
}

func dataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func NewApp() (*App, error) {
	st, err := store.Open(filepath.Join(dataDir(), "ann2html.db"))
	if err != nil {
		return nil, err
	}
	a := &App{st: st, languages: "ja"}
	a.vocabPath, _ = st.Get(store.KeyVocabPath)
	if a.vocabPath == "" {
		if p := filepath.Join(dataDir(), "vocab.db"); fileExists(p) {
			a.vocabPath = p
		}
	}
	if l, _ := st.Get(store.KeyLanguages); l != "" {
		a.languages = l
	}
	a.profile, _ = st.Get(store.KeyActiveProfile)
	if raw, _ := st.Get(store.KeyYomitanOptions); raw != "" {
		if e, err := settings.Parse([]byte(raw)); err == nil {
			a.export = e
		}
	}
	a.refreshDictionaries()
	return a, nil
}

func (a *App) Close() { a.st.Close() }

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

func (a *App) logf(format string, args ...any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log = append(a.log, time.Now().Format("15:04:05 ")+fmt.Sprintf(format, args...))
}

func (a *App) refreshDictionaries() {
	dicts, err := a.st.Dictionaries()
	if err != nil {
		a.logf("Error reading dictionaries: %v", err)
		return
	}
	a.mu.Lock()
	a.dicts = dicts
	a.mu.Unlock()
}

func (a *App) Snapshot() Snapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Snapshot{
		VocabPath: a.vocabPath, Languages: a.languages, RescanAll: a.rescanAll,
		Busy: a.busy, Progress: a.progress, ProgressText: a.progressText,
		Status: a.status, Log: append([]string(nil), a.log...),
	}
	imported := map[string]bool{}
	for _, d := range a.dicts {
		imported[d.Title] = true
	}
	enabled := map[string]bool{}
	if a.export != nil {
		s.Profiles = a.export.ProfileNames()
		p := a.export.Profile(a.profile)
		for i, name := range s.Profiles {
			if name == p.Name {
				s.ProfileIndex = i
			}
		}
		if f, err := p.Options.Anki.TermFormat(); err == nil {
			s.Deck, s.Model = f.Deck, f.Model
		} else {
			s.Warnings = append(s.Warnings, err.Error())
		}
		if p.Options.Anki.HasCustomTemplates() {
			s.Warnings = append(s.Warnings, "Custom Anki field templates are not supported; Yomitan's default templates are used.")
		}
		var missing []string
		for _, d := range p.Options.Dictionaries.Enabled() {
			enabled[d.Name] = true
			if !imported[d.Name] {
				missing = append(missing, d.Name)
			}
		}
		if len(missing) > 0 {
			s.Warnings = append(s.Warnings, "Not imported yet: "+strings.Join(missing, ", "))
		}
	}
	for _, d := range a.dicts {
		line := fmt.Sprintf("%s  (%d terms)", d.Title, d.Terms)
		if a.export != nil && !enabled[d.Title] {
			line += "  - not enabled in profile"
		}
		s.Dicts = append(s.Dicts, line)
	}
	return s
}

// start runs task in the background unless another task is running.
func (a *App) start(name string, task func(ctx context.Context) error) {
	a.mu.Lock()
	if a.busy != "" {
		a.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.busy, a.cancel, a.progress, a.progressText = name, cancel, 0, ""
	a.mu.Unlock()
	go func() {
		err := task(ctx)
		if err != nil {
			a.logf("%s failed: %v", name, err)
		}
		a.mu.Lock()
		a.busy, a.cancel = "", nil
		a.mu.Unlock()
		cancel()
	}()
}

func (a *App) Cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		a.cancel()
	}
}

func (a *App) SetVocabPath(p string) {
	a.mu.Lock()
	a.vocabPath = p
	a.mu.Unlock()
	a.st.Set(store.KeyVocabPath, p)
	a.logf("Using vocab.db: %s", p)
}

func (a *App) SetLanguages(l string) {
	a.mu.Lock()
	a.languages = l
	a.mu.Unlock()
	a.st.Set(store.KeyLanguages, l)
}

func (a *App) SetRescanAll(v bool) {
	a.mu.Lock()
	a.rescanAll = v
	a.mu.Unlock()
}

func (a *App) SetProfile(i int) {
	a.mu.Lock()
	if a.export == nil || i < 0 || i >= len(a.export.Options.Profiles) {
		a.mu.Unlock()
		return
	}
	name := a.export.Options.Profiles[i].Name
	changed := a.profile != name
	a.profile = name
	a.mu.Unlock()
	if changed {
		a.st.Set(store.KeyActiveProfile, name)
		a.logf("Using Yomitan profile %q", name)
	}
}

func (a *App) ImportSettings(path string) {
	a.start("Importing settings", func(ctx context.Context) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		e, err := settings.Parse(data)
		if err != nil {
			return err
		}
		if err := a.st.Set(store.KeyYomitanOptions, string(data)); err != nil {
			return err
		}
		current := e.Profile("").Name
		a.st.Set(store.KeyActiveProfile, current)
		a.mu.Lock()
		a.export, a.profile = e, current
		a.mu.Unlock()
		a.logf("Imported Yomitan settings with %d profile(s); using %q", len(e.Options.Profiles), current)
		return nil
	})
}

func (a *App) ImportDictionaries(paths []string) {
	a.start("Importing dictionaries", func(ctx context.Context) error {
		for i, p := range paths {
			name := filepath.Base(p)
			a.logf("Importing %s", name)
			prefix := fmt.Sprintf("[%d/%d] ", i+1, len(paths))
			err := importer.Import(a.st, p, func(msg string, done, total int64) {
				a.mu.Lock()
				defer a.mu.Unlock()
				a.progressText = prefix + msg
				if total > 0 {
					a.progress = float32(done) / float32(total)
				}
			})
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
		a.refreshDictionaries()
		a.logf("Dictionary import finished")
		return nil
	})
}

func (a *App) Mine() {
	a.mu.Lock()
	cfg := miner.Config{Store: a.st, VocabPath: a.vocabPath, Settings: a.export, Profile: a.profile, RescanAll: a.rescanAll}
	languages := a.languages
	a.status = miner.Status{}
	a.minerLogSeen = 0
	a.mu.Unlock()

	langs, err := kindle.ParseLanguages(languages)
	if err != nil {
		a.logf("%v", err)
		return
	}
	cfg.Languages = langs
	if cfg.VocabPath == "" {
		a.logf("Select your vocab.db first")
		return
	}
	a.start("Mining", func(ctx context.Context) error {
		status, err := miner.Run(ctx, cfg, a.onMinerStatus)
		a.onMinerStatus(status)
		if err == nil {
			a.logf("Finished: %d lookups, %d added, %d already in Anki, %d without definition, %d failed",
				status.Total, status.Added, status.Duplicates, status.NoDefinition, status.Failed)
		}
		return err
	})
}

func (a *App) onMinerStatus(s miner.Status) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = s
	for _, line := range s.Log[min(a.minerLogSeen, len(s.Log)):] {
		a.log = append(a.log, time.Now().Format("15:04:05 ")+line)
	}
	a.minerLogSeen = len(s.Log)
	if s.Total > 0 {
		a.progress = float32(s.Processed) / float32(s.Total)
	}
	a.progressText = s.Phase
}

// HandleDroppedFiles routes files dropped on the window by type.
func (a *App) HandleDroppedFiles(paths []string) {
	var dicts []string
	for _, p := range paths {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".db", ".sqlite":
			a.SetVocabPath(p)
		case ".zip":
			dicts = append(dicts, p)
		case ".json":
			if isDictionaryCollection(p) {
				dicts = append(dicts, p)
			} else {
				a.ImportSettings(p)
			}
		default:
			a.logf("Don't know what to do with %s", filepath.Base(p))
		}
	}
	if len(dicts) > 0 {
		a.ImportDictionaries(dicts)
	}
}

// isDictionaryCollection peeks at a JSON file to tell a dictionary
// collection export (Dexie) from a settings export.
func isDictionaryCollection(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := bufio.NewReader(f).Read(buf)
	return strings.Contains(string(buf[:n]), `"dexie"`)
}
