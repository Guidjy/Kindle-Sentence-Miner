package japanese

import (
	"reflect"
	"testing"
)

func TestDistributeFurigana(t *testing.T) {
	cases := []struct {
		term, reading string
		want          []Segment
	}{
		{"食べる", "たべる", []Segment{{"食", "た"}, {"べる", ""}}},
		{"お茶", "おちゃ", []Segment{{"お", ""}, {"茶", "ちゃ"}}},
		{"取り扱い", "とりあつかい", []Segment{{"取", "と"}, {"り", ""}, {"扱", "あつか"}, {"い", ""}}},
		{"かな", "かな", []Segment{{"かな", ""}}},
		{"日本", "にほん", []Segment{{"日本", "にほん"}}},
		{"カタカナ", "かたかな", []Segment{{"カタカナ", "かたかな"}}},
	}
	for _, c := range cases {
		if got := DistributeFurigana(c.term, c.reading); !reflect.DeepEqual(got, c.want) {
			t.Errorf("DistributeFurigana(%q, %q) = %v, want %v", c.term, c.reading, got, c.want)
		}
	}
}

func TestDistributeFuriganaInflected(t *testing.T) {
	got := DistributeFuriganaInflected("食べる", "たべる", "食べた")
	want := []Segment{{"食", "た"}, {"べた", ""}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestMoraeAndPitch(t *testing.T) {
	if got := GetKanaMorae("きょうと"); !reflect.DeepEqual(got, []string{"きょ", "う", "と"}) {
		t.Errorf("morae = %v", got)
	}
	if c := GetPitchCategory("はし", Pitch{Position: 2}, false); c != "odaka" {
		t.Errorf("category = %q", c)
	}
	if c := GetPitchCategory("たべる", Pitch{Position: 2}, true); c != "kifuku" {
		t.Errorf("category = %q", c)
	}
	if KatakanaToHiragana("カード", false) != "かあど" {
		t.Errorf("prolonged conversion = %q", KatakanaToHiragana("カード", false))
	}
}
