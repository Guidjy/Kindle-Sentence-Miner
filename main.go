// Command ann2html mines Kindle Vocabulary Builder lookups into Anki cards
// that match the user's Yomitan setup.
package main

import (
	_ "embed"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	rg "github.com/gen2brain/raylib-go/raygui"
	rl "github.com/gen2brain/raylib-go/raylib"
	"github.com/ncruces/zenity"
)

//go:embed assets/MPLUS1p-Regular.ttf
var fontData []byte

const (
	textSize   = 18
	fontSize   = 36 // rasterized larger and scaled down for crisp text
	pad        = 16
	rowHeight  = 30
	windowName = "Kindle Sentence Miner"
)

func init() {
	// raylib must run on the main OS thread.
	runtime.LockOSThread()
}

// fontCache rebuilds the GUI font whenever text with new characters (e.g.
// Japanese words in the log) needs to be drawn, keeping the atlas small.
type fontCache struct {
	font   rl.Font
	runes  map[rune]bool
	loaded bool
}

func newFontCache() *fontCache {
	f := &fontCache{runes: map[rune]bool{}}
	for r := rune(32); r < 127; r++ {
		f.runes[r] = true
	}
	for r := rune(0x3000); r <= 0x30ff; r++ { // CJK punctuation, hiragana, katakana
		f.runes[r] = true
	}
	return f
}

func (f *fontCache) ensure(texts ...[]string) {
	changed := !f.loaded
	for _, list := range texts {
		for _, s := range list {
			for _, r := range s {
				if !f.runes[r] {
					f.runes[r] = true
					changed = true
				}
			}
		}
	}
	if !changed {
		return
	}
	codepoints := make([]rune, 0, len(f.runes))
	for r := range f.runes {
		codepoints = append(codepoints, r)
	}
	if f.loaded {
		rl.UnloadFont(f.font)
	}
	f.font = rl.LoadFontFromMemory(".ttf", fontData, fontSize, codepoints)
	rl.SetTextureFilter(f.font.Texture, rl.FilterBilinear)
	rg.SetFont(f.font)
	f.loaded = true
}

func (f *fontCache) unload() {
	if f.loaded {
		rl.UnloadFont(f.font)
	}
}

type ui struct {
	app          *App
	dialogOpen   atomic.Bool
	profileEdit  bool
	profileIndex int32
	langEdit     bool
	langText     string
	logScroll    int32
	logActive    int32
	logFocus     int32
	logLen       int
	dictScroll   int32
	dictActive   int32
	dictFocus    int32
}

// dialog runs a blocking file dialog without freezing the window.
func (u *ui) dialog(fn func()) {
	if !u.dialogOpen.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer u.dialogOpen.Store(false)
		fn()
	}()
}

func (u *ui) selectVocab() {
	u.dialog(func() {
		p, err := zenity.SelectFile(zenity.Title("Select your Kindle's vocab.db"),
			zenity.FileFilters{{Name: "Kindle vocabulary database", Patterns: []string{"*.db"}, CaseFold: true}})
		if err == nil {
			u.app.SetVocabPath(p)
		}
	})
}

func (u *ui) importSettings() {
	u.dialog(func() {
		p, err := zenity.SelectFile(zenity.Title("Select a Yomitan settings export (Settings > Backup > Export Settings)"),
			zenity.FileFilters{{Name: "Yomitan settings", Patterns: []string{"*.json"}, CaseFold: true}})
		if err == nil {
			u.app.ImportSettings(p)
		}
	})
}

func (u *ui) importDictionaries() {
	u.dialog(func() {
		paths, err := zenity.SelectFileMultiple(zenity.Title("Select a Yomitan dictionary collection export or dictionary .zip files"),
			zenity.FileFilters{{Name: "Yomitan dictionaries", Patterns: []string{"*.json", "*.zip"}, CaseFold: true}})
		if err == nil && len(paths) > 0 {
			u.app.ImportDictionaries(paths)
		}
	})
}

func main() {
	app, err := NewApp()
	if err != nil {
		zenity.Error(fmt.Sprintf("Could not open the application database: %v", err), zenity.Title(windowName))
		return
	}
	defer app.Close()

	rl.SetConfigFlags(rl.FlagWindowResizable | rl.FlagMsaa4xHint)
	rl.InitWindow(960, 720, windowName)
	defer rl.CloseWindow()
	rl.SetWindowMinSize(760, 600)
	rl.SetTargetFPS(30)

	fonts := newFontCache()
	defer fonts.unload()
	rg.SetStyle(rg.DEFAULT, rg.TEXT_SIZE, textSize)
	rg.SetStyle(rg.LISTVIEW, rg.LIST_ITEMS_HEIGHT, 24)
	rg.SetStyle(rg.LISTVIEW, rg.TEXT_ALIGNMENT, rg.TEXT_ALIGN_LEFT)
	rg.SetStyle(rg.LISTVIEW, rg.TEXT_PADDING, 8)

	u := &ui{app: app, langText: app.Snapshot().Languages, logActive: -1, logFocus: -1, dictActive: -1, dictFocus: -1}
	for !rl.WindowShouldClose() {
		if rl.IsFileDropped() {
			app.HandleDroppedFiles(rl.LoadDroppedFiles())
		}
		s := app.Snapshot()
		fonts.ensure(s.Log, s.Dicts, s.Profiles, s.Warnings, []string{s.VocabPath, s.Deck, s.Model, s.ProgressText, s.Busy})

		rl.BeginDrawing()
		rl.ClearBackground(rl.GetColor(uint(rg.GetStyle(rg.DEFAULT, rg.BACKGROUND_COLOR))))
		u.draw(s)
		rl.EndDrawing()
	}
	app.Cancel()
}

func label(x, y, w float32, text string) {
	rg.Label(rl.NewRectangle(x, y, w, rowHeight), text)
}

func (u *ui) draw(s Snapshot) {
	w := float32(rl.GetScreenWidth())
	h := float32(rl.GetScreenHeight())
	x, inner := float32(pad), w-2*pad
	busy := s.Busy != "" || u.dialogOpen.Load()
	y := float32(pad)

	// 1. vocab.db
	rg.GroupBox(rl.NewRectangle(x, y, inner, 64), "1. Kindle vocab.db")
	setEnabled(!busy)
	if rg.Button(rl.NewRectangle(x+12, y+18, 180, rowHeight), "Select vocab.db...") {
		u.selectVocab()
	}
	setEnabled(true)
	path := s.VocabPath
	if path == "" {
		path = "Not selected (copy it from your Kindle's system/vocabulary folder)"
	}
	label(x+204, y+18, inner-216, path)
	y += 80

	// 2. Yomitan settings
	rg.GroupBox(rl.NewRectangle(x, y, inner, 98), "2. Yomitan settings")
	setEnabled(!busy)
	if rg.Button(rl.NewRectangle(x+12, y+18, 180, rowHeight), "Import settings...") {
		u.importSettings()
	}
	setEnabled(true)
	profileBox := rl.NewRectangle(x+204, y+18, 240, rowHeight)
	if len(s.Profiles) == 0 {
		label(x+204, y+18, inner-216, "Export them in Yomitan: Settings > Backup > Export Settings")
	} else {
		label(x+456, y+18, inner-468, fmt.Sprintf("Deck: %s   Note type: %s", orDash(s.Deck), orDash(s.Model)))
	}
	warning := strings.Join(s.Warnings, "  ")
	if warning != "" {
		rg.SetStyle(rg.LABEL, rg.TEXT_COLOR_NORMAL, rg.PropertyValue(rl.ColorToInt(rl.Maroon)))
		label(x+12, y+56, inner-24, warning)
		rg.SetStyle(rg.LABEL, rg.TEXT_COLOR_NORMAL, rg.GetStyle(rg.DEFAULT, rg.TEXT_COLOR_NORMAL))
	} else if len(s.Profiles) > 0 {
		label(x+12, y+56, inner-24, "Cards will use this profile's card format, dictionaries, audio sources and duplicate settings.")
	}
	y += 114

	// 3. Dictionaries
	dictHeight := float32(150)
	rg.GroupBox(rl.NewRectangle(x, y, inner, dictHeight), "3. Dictionaries")
	setEnabled(!busy)
	if rg.Button(rl.NewRectangle(x+12, y+18, 180, rowHeight), "Import dictionaries...") {
		u.importDictionaries()
	}
	setEnabled(true)
	label(x+204, y+18, inner-216, "Yomitan: Settings > Backup > Export Dictionary Collection, or dictionary .zip files")
	dicts := s.Dicts
	if len(dicts) == 0 {
		dicts = []string{"No dictionaries imported"}
	}
	u.dictActive = -1 // the list is informational
	listView(rl.NewRectangle(x+12, y+54, inner-24, dictHeight-66), dicts, &u.dictScroll, &u.dictActive, &u.dictFocus)
	y += dictHeight + 16

	// 4. Mine
	rg.GroupBox(rl.NewRectangle(x, y, inner, 104), "4. Create cards")
	label(x+12, y+18, 90, "Languages:")
	setEnabled(!busy)
	if rg.TextBox(rl.NewRectangle(x+104, y+18, 120, rowHeight), &u.langText, 64, u.langEdit) {
		u.langEdit = !u.langEdit
		if !u.langEdit {
			u.app.SetLanguages(u.langText)
		}
	}
	rescan := s.RescanAll
	rg.CheckBox(rl.NewRectangle(x+244, y+24, 18, 18), "Re-scan all lookups (already mined cards are skipped)", &rescan)
	if rescan != s.RescanAll {
		u.app.SetRescanAll(rescan)
	}
	setEnabled(!busy && s.VocabPath != "" && len(s.Profiles) > 0)
	if rg.Button(rl.NewRectangle(w-pad-12-180, y+18, 180, rowHeight), "Mine cards") {
		if u.langEdit {
			u.langEdit = false
			u.app.SetLanguages(u.langText)
		}
		u.app.Mine()
	}
	setEnabled(true)
	st := s.Status
	label(x+12, y+58, inner-24, fmt.Sprintf("Lookups found: %d     Added: %d     Already in Anki: %d     No definition: %d     Failed: %d",
		st.Total, st.Added, st.Duplicates, st.NoDefinition, st.Failed))
	y += 120

	// Progress
	progress := s.Progress
	text := s.ProgressText
	if s.Busy != "" && text == "" {
		text = s.Busy
	}
	if s.Busy == "" && text == "" {
		text = "Idle"
	}
	rg.ProgressBar(rl.NewRectangle(x+110, y, inner-110-110, 22), "", "", &progress, 0, 1)
	label(x, y-4, 104, "Progress")
	if s.Busy != "" {
		if rg.Button(rl.NewRectangle(w-pad-100, y-4, 100, rowHeight), "Cancel") {
			u.app.Cancel()
		}
	}
	y += 26
	label(x+110, y, inner-110, text)
	y += 30

	// Log
	logHeight := h - y - pad
	if logHeight < 80 {
		logHeight = 80
	}
	lines := s.Log
	if len(lines) == 0 {
		lines = []string{"Drop vocab.db, a Yomitan settings export or dictionaries on this window, or use the buttons above."}
	}
	if len(s.Log) != u.logLen {
		visible := int32(logHeight / 28)
		u.logScroll = max(0, int32(len(lines))-visible+1)
		u.logLen = len(s.Log)
		u.logActive = -1
	}
	listView(rl.NewRectangle(x, y, inner, logHeight), lines, &u.logScroll, &u.logActive, &u.logFocus)

	// Dropdowns are drawn last so they overlay other controls.
	if len(s.Profiles) > 0 {
		if !u.profileEdit {
			u.profileIndex = int32(s.ProfileIndex)
		}
		setEnabled(!busy)
		if rg.DropdownBox(profileBox, strings.Join(s.Profiles, ";"), &u.profileIndex, u.profileEdit) {
			u.profileEdit = !u.profileEdit
			if !u.profileEdit {
				u.app.SetProfile(int(u.profileIndex))
			}
		}
		setEnabled(true)
	}
}

// listView wraps rg.ListViewEx, whose Go binding forwards its pointers to
// GuiListViewEx(bounds, text, count, scrollIndex, active, focus) in the order
// (focus, scrollIndex, active). Passing them rotated puts each in the right slot.
func listView(bounds rl.Rectangle, items []string, scrollIndex, active, focus *int32) {
	rg.ListViewEx(bounds, items, scrollIndex, active, focus)
}

func setEnabled(enabled bool) {
	if enabled {
		rg.Enable()
	} else {
		rg.Disable()
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return filepath.ToSlash(s)
}
