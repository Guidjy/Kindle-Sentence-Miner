# ann2html — Kindle Sentence Miner

Turns the words you look up on your Kindle into Anki cards, automatically. The cards use your
Yomitan setup: the same deck, note type, field markers, dictionaries (in the same order), audio
sources and duplicate rules, so they look like cards you mined by hand with Yomitan.

## Features
* Small desktop GUI, a single portable executable; nothing to install.
* Reads your Kindle's `vocab.db` (Vocabulary Builder) and only mines lookups made since the last run.
* Imports your Yomitan settings export and your dictionaries (the "dictionary collection" export, or
  individual Yomitan dictionary `.zip` files) into a local SQLite database indexed for fast lookups.
* Looks words up the way Yomitan does when you hover them: it finds the word in its sentence, scans
  from there with Yomitan's own deinflection rules (run in an embedded JavaScript engine), and uses
  the enabled dictionaries in profile order, the profile's result grouping mode (group / split /
  merge / term), tags, frequencies and pitch accents. Inflected words (e.g. いたわって) are
  highlighted in the sentence.
* Renders every field with Yomitan's default Anki marker output (`{glossary}`, `{furigana}`,
  `{cloze-body}`, `{sentence-furigana}`, `{conjugation}`, `{pitch-accents}`, `{frequencies}`, `{audio}`,
  per-dictionary `{single-glossary-…}`…),
  including structured-content dictionaries, dictionary styles and dictionary images.
* Downloads audio from the profile's audio sources (JapanesePod101, custom URL, custom JSON / local
  audio server, Jisho, LanguagePod101, Lingua Libre, Wiktionary).
* Highlights every occurrence of the mined word in the sentence when your card format wraps
  `{cloze-body}` in a tag (e.g. `{cloze-prefix}<b>{cloze-body}</b>{cloze-suffix}`). The same tag
  highlights the word in `{sentence-furigana}` fields.
* Never creates duplicates: words already in Anki (using the profile's duplicate scope) and words
  looked up several times are skipped.

## Before you start
1. Install the [AnkiConnect](https://ankiweb.net/shared/info/2055492159) add-on and keep Anki open
   while mining.
2. In Yomitan, export your settings: **Settings → Backup → Export Settings**.
3. In Yomitan, export your dictionaries: **Settings → Backup → Export Dictionary Collection**. You can
   import the original dictionary `.zip` files instead.

## Usage
Run `ann2html` and work through the window from top to bottom:

1. **Select vocab.db.** Copy it from your Kindle or select it on the mounted Kindle:

   | Operating system | vocab.db location |
   | ---------------- | ----------------- |
   | Windows | KINDLEDRIVELETTER:\system\vocabulary\vocab.db |
   | macOS | MOUNTPOINT/system/vocabulary/vocab.db |
   | Linux | MOUNTPOINT/system/vocabulary/vocab.db |

   Windows hides this folder in an odd way; if you can't find it, search the Kindle drive for
   `vocab.db`.
2. **Import settings** (once, or again after changing Yomitan). If your export has several profiles,
   pick the one to use from the dropdown.
3. **Import dictionaries** (once). A full collection export can take a few minutes. Importing a
   collection replaces all previously imported dictionaries. If you imported dictionaries with an
   earlier version of this app, re-import the collection (the window shows a warning): earlier
   versions lost pitch accents, frequencies and dictionary styles from recent Yomitan exports.
4. Click **Mine cards**. Only lookups in your Yomitan profile's language are mined. The window
   shows how many lookups were found, added, already in Anki, without a definition, or failed.

You can also drag and drop `vocab.db`, a settings export or dictionaries onto the window.

To mine by hand instead, click **Open sentences page**. It writes `edit.html` next to the executable
(the original ann2html page: every lookup's sentence with the word in bold, oldest first) and opens
it in your browser, where you can mine with Yomitan as usual. Press `b` on the page to bookmark your
position; new lookups are added at the end, so the bookmark stays valid. This only needs
`vocab.db`, not the Yomitan settings or dictionaries.

The app remembers where it stopped: the next run only looks at new lookups. If adding a card fails
(for example Anki was closed), that lookup is retried on the next run. Tick **Re-scan all lookups** to
go through the whole `vocab.db` again, for example after importing a new dictionary; cards already in
Anki are still skipped.

All data (imported dictionaries, settings and progress) is stored in `ann2html.db` next to the
executable.

### Limitations
* Customized Anki field templates (Yomitan's handlebars templates) are not supported; the default
  templates are used, and the app warns you if your profile has customized ones.
* Text-to-speech audio sources and the `{pitch-accent-graphs-jj}`, `{screenshot}` and clipboard markers
  are not supported.
* The card uses the word Yomitan would show at the spot the Kindle lookup points to.
* If you selected several words on the Kindle (e.g. は、潔く or として雇う), the lookup is skipped,
  because it is unclear which word you meant. Skipped lookups are listed in the log and counted as
  "Multi-word"; mine them by hand from the sentences page. Selecting part of a word (咎めなかっ) or a
  word with trailing grammar (参ったな) still creates a card.
* The duplicate behavior is always "prevent": existing notes are never overwritten.

### Linux script
For Linux users there is an optional script called `ann` that mounts your Kindle, opens ann2html and
unmounts the Kindle when you close it. It depends on `udisksctl`. Add your Kindle's UUID to the
script, make it executable and put it in your PATH.

To find your UUID run `ls -l /dev/disk/by-uuid`, then connect your Kindle and run it again; the new
entry is your Kindle. Then set it in the script:
```
kindle_UUID=3582-6578
```

## Building from source
The GUI uses [raylib-go](https://github.com/gen2brain/raylib-go), which needs cgo and a C compiler:
* Windows: a MinGW-w64 gcc (e.g. from MSYS2) in your PATH.
* Linux: gcc plus the X11/OpenGL development packages (`libgl1-mesa-dev libxi-dev libxcursor-dev
  libxrandr-dev libxinerama-dev`, or your distribution's equivalent).
* macOS: the Xcode command line tools.

```
go build .            # or: make build
go test ./...         # or: make test
```
`make dist` builds the release archives; each target has to be built on its own OS, or with a cross C
compiler set in `CC`.

## License
GPL-3.0. The card rendering ports parts of [Yomitan](https://github.com/yomidevs/yomitan) (GPL-3.0).
The bundled M PLUS 1p font is licensed under the SIL Open Font License (`assets/OFL.txt`).
