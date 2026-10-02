package sentencepage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xythh/ann2html/internal/deinflect"
	"github.com/xythh/ann2html/internal/kindle"
)

func TestSentence(t *testing.T) {
	d, err := deinflect.New()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		lk   kindle.Lookup
		want string
	}{
		{kindle.Lookup{Lemma: "いたわる", Surface: "いたわっ", Usage: "青子の健康をいたわって休みを入れる"},
			"青子の健康を<b>いたわって</b>休みを入れる"},
		{kindle.Lookup{Lemma: "食べる", Surface: "食べ", Usage: "食べる前に食べたのに、また食べた。"},
			"<b>食べる</b>前に<b>食べた</b>のに、また<b>食べた</b>。"},
		{kindle.Lookup{Lemma: "家名", Surface: "家名", Usage: "家名に泥が付く<から>"},
			"<b>家名</b>に泥が付く&lt;から&gt;"},
		{kindle.Lookup{Lemma: "無い語", Surface: "無い語", Usage: "関係ない文"},
			"関係ない文"},
	}
	for _, c := range cases {
		if got := Sentence(c.lk, d); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.lk.Lemma, got, c.want)
		}
	}
}

func TestWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	lookups := []kindle.Lookup{{Lemma: "猫", Surface: "猫", Usage: "猫がいる。"}}
	if err := Write(p, lookups, nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	got := string(b)
	if !strings.HasPrefix(got, template) {
		t.Error("page does not start with the original template")
	}
	if want := "<p> <b>猫</b>がいる。 </p>\n</body>\n</html>\n"; !strings.HasSuffix(got, want) {
		t.Errorf("page ends with %q", got[len(template):])
	}
}
