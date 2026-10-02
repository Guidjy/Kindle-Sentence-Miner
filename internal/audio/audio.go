// Package audio downloads term audio from the audio sources configured in a
// Yomitan profile (port of ext/js/media/audio-downloader.js).
package audio

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/xythh/ann2html/internal/japanese"
	"github.com/xythh/ann2html/internal/yomitan/settings"
)

// invalidJpod101 is the SHA-256 of the clip jpod101 serves when it has no audio.
const invalidJpod101 = "ae6398b5a27bc8c0a771df6c907ade794be15518174773c58c7c7ddd17098906"

// ErrNoAudio means no source had audio for the term.
var ErrNoAudio = errors.New("no audio found")

type Language struct {
	ISO     string // ISO 639-1, e.g. "ja"
	ISO6393 string // ISO 639-3, e.g. "jpn"
	Name    string // English name, e.g. "Japanese"
}

var languages = map[string]Language{
	"ja": {"ja", "jpn", "Japanese"}, "en": {"en", "eng", "English"}, "zh": {"zh", "zho", "Chinese"},
	"ko": {"ko", "kor", "Korean"}, "es": {"es", "spa", "Spanish"}, "fr": {"fr", "fra", "French"},
	"de": {"de", "deu", "German"}, "it": {"it", "ita", "Italian"}, "pt": {"pt", "por", "Portuguese"},
	"ru": {"ru", "rus", "Russian"},
}

func LanguageFor(iso string) Language {
	if l, ok := languages[iso]; ok {
		return l
	}
	return Language{ISO: iso, ISO6393: iso, Name: iso}
}

type Downloader struct {
	Sources  []settings.AudioSource
	Language Language
	HTTP     *http.Client
}

// New creates a downloader for a profile's audio settings. Like Yomitan, the
// default sources for the language are tried after the configured ones when
// enableDefaultAudioSources is set.
func New(audio settings.Audio, language string) *Downloader {
	if language == "" {
		language = "ja"
	}
	sources := append([]settings.AudioSource{}, audio.Sources...)
	if audio.EnableDefaultAudioSources {
		required := []string{"jpod101", "language-pod-101", "jisho"}
		if language != "ja" {
			required = []string{"lingua-libre", "language-pod-101", "wiktionary"}
		}
		for _, r := range required {
			found := false
			for _, s := range sources {
				if s.Type == r {
					found = true
				}
			}
			if !found {
				sources = append(sources, settings.AudioSource{Type: r})
			}
		}
	}
	return &Downloader{Sources: sources, Language: LanguageFor(language), HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// Audio is a downloaded audio file.
type Audio struct {
	Data     []byte
	FileName string // Yomitan style: yomitan_audio_<sha1>.<ext>
}

// Download returns the first valid audio for term/reading.
func (d *Downloader) Download(ctx context.Context, term, reading string) (*Audio, error) {
	var errs []error
	for _, src := range d.Sources {
		urls, err := d.urls(ctx, src, term, reading)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.Type, err))
			continue
		}
		for _, u := range urls {
			a, err := d.fetch(ctx, u, src.Type)
			if err == nil {
				return a, nil
			}
			errs = append(errs, fmt.Errorf("%s: %w", src.Type, err))
		}
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("%w: %w", ErrNoAudio, errors.Join(errs...))
	}
	return nil, ErrNoAudio
}

func (d *Downloader) urls(ctx context.Context, src settings.AudioSource, term, reading string) ([]string, error) {
	switch src.Type {
	case "jpod101":
		if reading == term && japanese.IsStringEntirelyKana(term) {
			term = ""
		}
		q := url.Values{}
		if term != "" {
			q.Set("kanji", term)
		}
		if reading != "" {
			q.Set("kana", reading)
		}
		return []string{"https://assets.languagepod101.com/dictionary/japanese/audiomp3.php?" + q.Encode()}, nil
	case "custom":
		if src.URL == "" {
			return nil, errors.New("no custom URL defined")
		}
		return []string{d.customURL(src.URL, term, reading)}, nil
	case "custom-json":
		if src.URL == "" {
			return nil, errors.New("no custom URL defined")
		}
		return d.customJSON(ctx, d.customURL(src.URL, term, reading))
	case "jisho":
		return d.jisho(ctx, term, reading)
	case "language-pod-101":
		return d.languagePod101(ctx, term, reading)
	case "lingua-libre":
		cat := url.QueryEscape(`incategory:"Lingua_Libre_pronunciation-` + d.Language.ISO6393 + `"`)
		search := "intitle:/-" + term + ".wav/i"
		re, err := regexp.Compile(`(?i)^File:LL-Q\d+\s+\(` + regexp.QuoteMeta(d.Language.ISO6393) + `\)-.+-` + regexp.QuoteMeta(term) + `\.wav$`)
		if err != nil {
			return nil, err
		}
		return d.wikimedia(ctx, url.QueryEscape(search)+"+"+cat, re)
	case "wiktionary":
		search := "intitle:/" + d.Language.ISO + "(-[a-zA-Z]{2})?-" + term + "[0123456789]*.ogg/i"
		re, err := regexp.Compile(`(?i)^File:` + regexp.QuoteMeta(d.Language.ISO) + `(-\w\w)?-` + regexp.QuoteMeta(term) + `\d*\.ogg$`)
		if err != nil {
			return nil, err
		}
		return d.wikimedia(ctx, url.QueryEscape(search), re)
	}
	// text-to-speech sources need a browser voice and are skipped.
	return nil, fmt.Errorf("audio source %q is not supported", src.Type)
}

var customPlaceholder = regexp.MustCompile(`\{([^}]*)\}`)

func (d *Downloader) customURL(tmpl, term, reading string) string {
	data := map[string]string{"term": term, "reading": reading, "language": d.Language.ISO}
	return customPlaceholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		if v, ok := data[m[1:len(m)-1]]; ok {
			return v
		}
		return m
	})
}

func (d *Downloader) get(ctx context.Context, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("invalid response: %d", resp.StatusCode)
	}
	return resp, nil
}

func (d *Downloader) customJSON(ctx context.Context, u string) ([]string, error) {
	resp, err := d.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list struct {
		Type         string `json:"type"`
		AudioSources []struct {
			URL string `json:"url"`
		} `json:"audioSources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, err
	}
	if list.Type != "audioSourceList" {
		return nil, errors.New("invalid custom audio list")
	}
	var out []string
	for _, s := range list.AudioSources {
		out = append(out, s.URL)
	}
	return out, nil
}

func resolve(base *url.URL, ref string) string {
	r, err := url.Parse(html.UnescapeString(ref))
	if err != nil {
		return ref
	}
	return base.ResolveReference(r).String()
}

var sourceSrc = regexp.MustCompile(`(?s)<source[^>]*\ssrc="([^"]+)"`)

func (d *Downloader) jisho(ctx context.Context, term, reading string) ([]string, error) {
	resp, err := d.get(ctx, "https://jisho.org/search/"+url.PathEscape(term))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	page := string(body)
	id := `id="audio_` + term + ":" + reading + `"`
	i := strings.Index(page, id)
	if i < 0 {
		return nil, errors.New("failed to find audio URL")
	}
	end := strings.Index(page[i:], "</audio>")
	if end < 0 {
		end = len(page) - i
	}
	m := sourceSrc.FindStringSubmatch(page[i : i+end])
	if m == nil {
		return nil, errors.New("failed to find audio URL")
	}
	return []string{resolve(resp.Request.URL, m[1])}, nil
}

var (
	pod101Row     = regexp.MustCompile(`(?s)class="[^"]*\bdc-result-row\b[^"]*"(.*?)(?:class="[^"]*\bdc-result-row\b|$)`)
	pod101Kana    = regexp.MustCompile(`(?s)class="[^"]*\bdc-vocab_kana\b[^"]*"[^>]*>(.*?)<`)
	pod101Vocab   = regexp.MustCompile(`(?s)class="[^"]*\bdc-vocab\b[^"]*"[^>]*>(.*?)<`)
	pod101Classes = map[string]string{
		"Cantonese": "class", "Chinese": "class", "Czech": "class", "Danish": "class", "English": "class",
		"Korean": "class", "Norwegian": "class", "Turkish": "class",
	}
)

func (d *Downloader) languagePod101(ctx context.Context, term, reading string) ([]string, error) {
	lang := d.Language.Name
	podOrClass := "pod"
	if c, ok := pod101Classes[lang]; ok {
		podOrClass = c
	}
	fetchURL := "https://www." + strings.ToLower(lang) + podOrClass + "101.com/learningcenter/reference/dictionary_post"
	form := url.Values{"post": {"dictionary_reference"}, "match_type": {"exact"}, "search_query": {term}, "vulgar": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fetchURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := d.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, row := range pod101Row.FindAllStringSubmatch(string(body), -1) {
		r := row[1]
		audioAt := strings.Index(r, "<audio")
		if audioAt < 0 {
			continue
		}
		m := sourceSrc.FindStringSubmatch(r[audioAt:])
		if m == nil {
			continue
		}
		if lang == "Japanese" {
			k := pod101Kana.FindStringSubmatch(r)
			if k == nil || k[1] == "" || (reading != term && reading != html.UnescapeString(strings.TrimSpace(k[1]))) {
				continue
			}
		} else {
			v := pod101Vocab.FindStringSubmatch(r)
			if v == nil || html.UnescapeString(strings.TrimSpace(v[1])) != term {
				continue
			}
		}
		u := resolve(resp.Request.URL, m[1])
		if !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	return out, nil
}

func (d *Downloader) wikimedia(ctx context.Context, search string, valid *regexp.Regexp) ([]string, error) {
	resp, err := d.get(ctx, "https://commons.wikimedia.org/w/api.php?action=query&format=json&list=search&srsearch="+search+"&srnamespace=6&origin=*")
	if err != nil {
		return nil, err
	}
	var results struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	err = json.NewDecoder(resp.Body).Decode(&results)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range results.Query.Search {
		if !valid.MatchString(r.Title) {
			continue
		}
		resp, err := d.get(ctx, "https://commons.wikimedia.org/w/api.php?action=query&format=json&titles="+url.QueryEscape(r.Title)+"&prop=imageinfo&iiprop=user|url&origin=*")
		if err != nil {
			continue
		}
		var info struct {
			Query struct {
				Pages map[string]struct {
					ImageInfo []struct {
						URL string `json:"url"`
					} `json:"imageinfo"`
				} `json:"pages"`
			} `json:"query"`
		}
		json.NewDecoder(resp.Body).Decode(&info)
		resp.Body.Close()
		for _, p := range info.Query.Pages {
			if len(p.ImageInfo) > 0 {
				out = append(out, p.ImageInfo[0].URL)
			}
		}
	}
	return out, nil
}

func (d *Downloader) fetch(ctx context.Context, u, sourceType string) (*Audio, error) {
	resp, err := d.get(ctx, u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if sourceType == "jpod101" {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) == invalidJpod101 {
			return nil, errors.New("could not retrieve audio")
		}
	}
	if len(data) == 0 {
		return nil, errors.New("empty audio file")
	}
	ext := extensionForAudio(resp.Header.Get("Content-Type"))
	sum := sha1.Sum(data)
	name := MediaFileName("yomitan_audio_", hex.EncodeToString(sum[:]), ext)
	return &Audio{Data: data, FileName: strings.ReplaceAll(name, "]", "")}, nil
}

func extensionForAudio(contentType string) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch mt {
	case "audio/aac":
		return ".aac"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/mp4":
		return ".mp4"
	case "audio/ogg", "audio/vorbis":
		return ".ogg"
	case "audio/vnd.wav", "audio/wave", "audio/wav", "audio/x-wav", "audio/x-pn-wav":
		return ".wav"
	case "audio/flac":
		return ".flac"
	case "audio/webm":
		return ".webm"
	}
	return ".mp3"
}

var invalidFileName = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1F]`)

// MediaFileName builds a Yomitan style media file name.
func MediaFileName(prefix, suffix, ext string) string {
	return invalidFileName.ReplaceAllString(prefix+suffix+ext, "-")
}
