package audio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xythh/ann2html/internal/yomitan/settings"
)

func TestCustomJSONSource(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			if r.URL.Query().Get("term") != "食べる" || r.URL.Query().Get("reading") != "たべる" {
				t.Errorf("bad query %s", r.URL.RawQuery)
			}
			json.NewEncoder(w).Encode(map[string]any{"type": "audioSourceList", "audioSources": []map[string]string{{"url": srv.URL + "/a.ogg"}}})
		case "/a.ogg":
			w.Header().Set("Content-Type", "audio/ogg")
			w.Write([]byte("OGG"))
		}
	}))
	defer srv.Close()
	d := New(settings.Audio{Sources: []settings.AudioSource{
		{Type: "custom", URL: srv.URL + "/missing?t={term}"},
		{Type: "custom-json", URL: srv.URL + "/list?term={term}&reading={reading}"},
	}}, "ja")
	a, err := d.Download(context.Background(), "食べる", "たべる")
	if err != nil {
		t.Fatal(err)
	}
	if string(a.Data) != "OGG" || !strings.HasPrefix(a.FileName, "yomitan_audio_") || !strings.HasSuffix(a.FileName, ".ogg") {
		t.Errorf("audio = %+v", a)
	}
}

func TestNoAudio(t *testing.T) {
	d := New(settings.Audio{Sources: []settings.AudioSource{{Type: "text-to-speech"}}}, "ja")
	if _, err := d.Download(context.Background(), "x", "x"); err == nil {
		t.Error("expected error")
	}
}
