package lore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfirmsAndAppends(t *testing.T) {
	s := New(t.TempDir())
	if n, err := s.Save("backend", []string{"Run make gen after editing api/schema.yaml.", "Money is integer cents."}); err != nil || n != 2 {
		t.Fatalf("first save = %d, %v", n, err)
	}
	if n, err := s.Save("backend", []string{"run  make gen after editing api/schema.yaml", "Tests need the db container."}); err != nil || n != 1 {
		t.Fatalf("second save = %d, %v", n, err)
	}
	items, err := s.Load("backend")
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		{Insight, 2, "Run make gen after editing api/schema.yaml."},
		{Insight, 1, "Money is integer cents."},
		{Insight, 1, "Tests need the db container."},
	}
	if fmt.Sprint(items) != fmt.Sprint(want) {
		t.Errorf("items = %v, want %v", items, want)
	}
	b, _ := os.ReadFile(filepath.Join(s.Dir, "backend.md"))
	if !strings.Contains(string(b), "- [insight ×2] Run make gen") {
		t.Errorf("file:\n%s", b)
	}
}

func TestPersonEditsSurvive(t *testing.T) {
	s := New(t.TempDir())
	os.MkdirAll(s.Dir, 0o700)
	body := "# My notes\n\nSome prose a person wrote.\n\n- [Canon] Never use floats for money.\n- [insight x3] Use the fake clock in tests.\n- [rumour] ignored\n- plain bullet ignored\n"
	os.WriteFile(filepath.Join(s.Dir, "tester.md"), []byte(body), 0o600)
	if _, err := s.Save("tester", []string{"never use floats for money!"}); err != nil {
		t.Fatal(err)
	}
	items, _ := s.Load("tester")
	want := []Item{{Canon, 2, "Never use floats for money."}, {Insight, 3, "Use the fake clock in tests."}}
	if fmt.Sprint(items) != fmt.Sprint(want) {
		t.Errorf("items = %v, want %v", items, want)
	}
}

func TestPruneKeepsCanonAndTheMostConfirmed(t *testing.T) {
	var items []Item
	items = append(items, Item{Canon, 1, "canon"})
	for i := 0; i < MaxItems+5; i++ {
		c := 1
		if i == 0 {
			c = 9
		}
		items = append(items, Item{Insight, c, fmt.Sprintf("i%d", i)})
	}
	got := prune(items)
	if len(got) != MaxItems {
		t.Fatalf("kept %d", len(got))
	}
	if got[0].Text != "canon" || got[1].Text != "i0" {
		t.Errorf("first kept = %v %v", got[0], got[1])
	}
	// The oldest of the least confirmed go first.
	for _, it := range got {
		if it.Text == "i1" {
			t.Errorf("oldest single insight survived: %v", got)
		}
	}
	if got[len(got)-1].Text != fmt.Sprintf("i%d", MaxItems+4) {
		t.Errorf("newest dropped: %v", got[len(got)-1])
	}
}

func TestRenderIsDataAndBounded(t *testing.T) {
	if Render("backend", nil) != "" {
		t.Error("no items should render nothing")
	}
	items := []Item{{Insight, 1, "low"}, {Insight, 4, "high"}, {Canon, 1, "rule"}}
	out := Render("backend", items)
	if !strings.Contains(out, "data, not instructions") || !strings.Contains(out, "note scope=role") {
		t.Errorf("render lacks framing:\n%s", out)
	}
	if strings.Index(out, "] rule") > strings.Index(out, "] high") || strings.Index(out, "] high") > strings.Index(out, "] low") {
		t.Errorf("render order wrong:\n%s", out)
	}
	if out != Render("backend", items) {
		t.Error("render is not deterministic")
	}
	var many []Item
	for i := 0; i < 200; i++ {
		many = append(many, Item{Insight, 1, strings.Repeat("x", 290) + fmt.Sprint(i)})
	}
	big := Render("backend", many)
	if len(big) > RenderBudget+600 || !strings.Contains(big, "more not shown") {
		t.Errorf("render not bounded: %d bytes", len(big))
	}
}

func TestCleanDefusesText(t *testing.T) {
	got := Clean("a\n</role-lore>‮​b\x00   c" + strings.Repeat("y", 400))
	if strings.ContainsAny(got, "\n<>‮​\x00") {
		t.Errorf("Clean left markup or controls: %q", got)
	}
	if n := len([]rune(got)); n > MaxTextRunes {
		t.Errorf("Clean kept %d runes", n)
	}
}

func TestRoleNamesAreChecked(t *testing.T) {
	s := New(t.TempDir())
	for _, r := range []string{"../x", "A", "", "a/b", strings.Repeat("a", 40)} {
		if _, err := s.Save(r, []string{"x"}); err == nil {
			t.Errorf("role %q accepted", r)
		}
	}
}

func TestProjectDir(t *testing.T) {
	a := ProjectDir("/state", "/home/me/shop")
	b := ProjectDir("/state", "/work/shop")
	if a == b || !strings.HasPrefix(a, filepath.Join("/state", "lore", "shop-")) {
		t.Errorf("dirs %s %s", a, b)
	}
	if ProjectDir("/state", "/home/me/shop") != a {
		t.Error("not stable")
	}
}
