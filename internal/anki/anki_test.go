package anki

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindDuplicatesFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string `json:"action"`
			Key    string `json:"key"`
			Params struct {
				Notes []Note `json:"notes"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Key != "secret" {
			t.Errorf("api key not sent")
		}
		switch req.Action {
		case "canAddNotesWithErrorDetail":
			json.NewEncoder(w).Encode(map[string]any{"result": nil, "error": "unsupported action"})
		case "canAddNotes":
			res := []bool{true, true}
			if !req.Params.Notes[0].Options.AllowDuplicate {
				res[1] = false // second note is a duplicate
			}
			json.NewEncoder(w).Encode(map[string]any{"result": res, "error": nil})
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "secret")
	dups, err := c.FindDuplicates(context.Background(), []Note{{}, {}})
	if err != nil {
		t.Fatal(err)
	}
	if dups[0] || !dups[1] {
		t.Errorf("dups = %v", dups)
	}
}

func TestFindDuplicatesDetailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"result": []map[string]any{
			{"canAdd": true},
			{"canAdd": false, "error": "cannot create note because it is a duplicate"},
			{"canAdd": false, "error": "model was not found"},
		}, "error": nil})
	}))
	defer srv.Close()
	dups, err := New(srv.URL, "").FindDuplicates(context.Background(), make([]Note, 3))
	if err != nil {
		t.Fatal(err)
	}
	if dups[0] || !dups[1] || dups[2] {
		t.Errorf("dups = %v", dups)
	}
}

func TestRootDeckName(t *testing.T) {
	if RootDeckName("Japanese::Mining") != "Japanese" || RootDeckName("Mining") != "Mining" {
		t.Error("RootDeckName")
	}
}
