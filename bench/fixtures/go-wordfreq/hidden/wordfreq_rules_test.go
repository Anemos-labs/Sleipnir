package wordfreq

import (
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// sameResult compares two results; an empty result is equal to a nil one.
func sameResult(t *testing.T, got, want []Count) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got  %v\n want %v", got, want)
	}
}

func TestRuleTiesAreAlphabetical(t *testing.T) {
	// Twenty-six words that occur once each, in reverse alphabetical order. The
	// result must not depend on map iteration order.
	text := "Zulu yankee xray whiskey victor uniform tango sierra romeo quebec papa " +
		"oscar november mike lima kilo juliet india hotel golf foxtrot echo delta " +
		"charlie bravo alpha"
	got := TopN(text, 10, nil)
	want := []Count{{"alpha", 1}, {"bravo", 1}, {"charlie", 1}, {"delta", 1}, {"echo", 1},
		{"foxtrot", 1}, {"golf", 1}, {"hotel", 1}, {"india", 1}, {"juliet", 1}}
	sameResult(t, got, want)
}

func TestRuleCountComesBeforeAlphabet(t *testing.T) {
	sameResult(t, TopN("b b b a c c", 3, nil), []Count{{"b", 3}, {"c", 2}, {"a", 1}})
	sameResult(t, TopN("x y y z z z", 2, nil), []Count{{"z", 3}, {"y", 2}})
}

func TestRuleTieBreakIsByteOrder(t *testing.T) {
	// "apple" < "zebra" < "zèbre" < "élan" when compared byte by byte.
	got := TopN("élan zebra apple Zèbre", 10, nil)
	want := []Count{{"apple", 1}, {"zebra", 1}, {"zèbre", 1}, {"élan", 1}}
	sameResult(t, got, want)
	// Upper case letters sort before lower case ones in the raw words, but words
	// are lower-cased first, so there is no difference here.
	sameResult(t, TopN("B a A b", 2, nil), []Count{{"a", 2}, {"b", 2}})
}

func TestRuleCaseFolding(t *testing.T) {
	sameResult(t, TopN("Go go GO gO", 5, nil), []Count{{"go", 4}})
	sameResult(t, TopN("Zulu ZULU Alpha", 5, nil), []Count{{"zulu", 2}, {"alpha", 1}})
}

func TestRuleUnicodeLetters(t *testing.T) {
	text := "Привет привет ПРИВЕТ мир Мир Ελλάδα ελλάδα 日本語 日本語 中文 ÀÉÎ àéî"
	got := TopN(text, 10, nil)
	// Among the words that occur twice the order is by bytes:
	// "àéî" (C3 ..) < "ελλάδα" (CE ..) < "мир" (D0 ..) < "日本語" (E6 ..).
	want := []Count{{"привет", 3}, {"àéî", 2}, {"ελλάδα", 2}, {"мир", 2}, {"日本語", 2}, {"中文", 1}}
	sameResult(t, got, want)
}

func TestRuleSeparators(t *testing.T) {
	tests := []struct {
		text string
		want []Count
	}{
		{"snake_case x1y2z3", []Count{{"case", 1}, {"snake", 1}, {"x", 1}, {"y", 1}, {"z", 1}}},
		{"tab\tsep\nnew\r\nline", []Count{{"line", 1}, {"new", 1}, {"sep", 1}, {"tab", 1}}},
		{"a.b,c;d:e!f?g", []Count{{"a", 1}, {"b", 1}, {"c", 1}, {"d", 1}, {"e", 1}, {"f", 1}, {"g", 1}}},
		{"(paren) [brk] {brc} \"quoted\" 100% ©2024 Acme™", []Count{{"acme", 1}, {"brc", 1}, {"brk", 1}, {"paren", 1}, {"quoted", 1}}},
		{"a\u00a0b\u2003c", []Count{{"a", 1}, {"b", 1}, {"c", 1}}},
		{"😀smile😀 fun", []Count{{"fun", 1}, {"smile", 1}}},
		{"stop-go stop--go", []Count{{"go", 2}, {"stop", 2}}},
		{"ab12ab 7 ab", []Count{{"ab", 3}}},
	}
	for _, tc := range tests {
		sameResult(t, TopN(tc.text, 100, nil), tc.want)
	}
}

func TestRuleApostrophes(t *testing.T) {
	tests := []struct {
		text string
		want []Count
	}{
		{"don't Don't DON'T", []Count{{"don't", 3}}},
		{"'quoted' dogs' it''s", []Count{{"dogs", 1}, {"it", 1}, {"quoted", 1}, {"s", 1}}},
		{"rock'n'roll", []Count{{"rock'n'roll", 1}}},
		{"a'b'c'", []Count{{"a'b'c", 1}}},
		{"x' y 'z", []Count{{"x", 1}, {"y", 1}, {"z", 1}}},
		{"o'clock o'Clock O'CLOCK", []Count{{"o'clock", 3}}},
		{"don't-stop", []Count{{"don't", 1}, {"stop", 1}}},
		{"a'1 1'a", []Count{{"a", 2}}},
		{"'", nil},
		{"'' ''' a ' b", []Count{{"a", 1}, {"b", 1}}},
	}
	for _, tc := range tests {
		sameResult(t, TopN(tc.text, 100, nil), tc.want)
	}
}

func TestRuleStopWords(t *testing.T) {
	text := "The cat and THE dog AND a bird"
	got := TopN(text, 10, []string{"the", "And"})
	want := []Count{{"a", 1}, {"bird", 1}, {"cat", 1}, {"dog", 1}}
	sameResult(t, got, want)

	// Stop words are removed before the top n is chosen.
	sameResult(t, TopN("a a a b b c", 2, []string{"a"}), []Count{{"b", 2}, {"c", 1}})

	// Case is ignored for non-ASCII letters too, and apostrophes work.
	sameResult(t, TopN("Élan élan Zèbre", 5, []string{"ÉLAN"}), []Count{{"zèbre", 1}})
	sameResult(t, TopN("Don't stop", 5, []string{"don't"}), []Count{{"stop", 1}})

	// Everything stopped, and an empty stop list.
	sameResult(t, TopN("the The THE", 5, []string{"the"}), nil)
	sameResult(t, TopN("a b", 5, []string{}), []Count{{"a", 1}, {"b", 1}})
	sameResult(t, TopN("a b", 5, nil), []Count{{"a", 1}, {"b", 1}})
}

func TestRuleStopIsNotModified(t *testing.T) {
	stop := []string{"The", "AND", "Élan"}
	before := slices.Clone(stop)
	TopN("the and élan cat", 3, stop)
	if !slices.Equal(stop, before) {
		t.Errorf("TopN modified its stop argument: %q, was %q", stop, before)
	}
}

func TestRuleHowMany(t *testing.T) {
	text := "a a a b b c"
	sameResult(t, TopN(text, 1, nil), []Count{{"a", 3}})
	sameResult(t, TopN(text, 2, nil), []Count{{"a", 3}, {"b", 2}})
	sameResult(t, TopN(text, 3, nil), []Count{{"a", 3}, {"b", 2}, {"c", 1}})
	sameResult(t, TopN(text, 100, nil), []Count{{"a", 3}, {"b", 2}, {"c", 1}})
	for _, n := range []int{0, -1, -100} {
		if got := TopN(text, n, nil); len(got) != 0 {
			t.Errorf("TopN(text, %d, nil) = %v, want an empty result", n, got)
		}
	}
}

func TestRuleNoWords(t *testing.T) {
	for _, text := range []string{"", " ", "123 456", "... !!! ???", "'''", "\n\t\r\n", "😀😀"} {
		if got := TopN(text, 5, nil); len(got) != 0 {
			t.Errorf("TopN(%q, 5, nil) = %v, want an empty result", text, got)
		}
	}
}

func TestRuleManyWords(t *testing.T) {
	const distinct = 300
	name := func(i int) string { // letters only, unique for i < 676: "aaa", "aab", ...
		return string([]rune{rune('a' + i/26/26%26), rune('a' + i/26%26), rune('a' + i%26)})
	}
	var want []Count
	var tokens []string
	for i := 0; i < distinct; i++ {
		c := (i*7)%13 + 1
		w := name(i)
		want = append(want, Count{w, c})
		for j := 0; j < c; j++ {
			tokens = append(tokens, strings.ToUpper(w[:1])+w[1:]) // written with a capital
		}
	}
	rand.New(rand.NewSource(7)).Shuffle(len(tokens), func(i, j int) { tokens[i], tokens[j] = tokens[j], tokens[i] })
	sort.Slice(want, func(i, j int) bool {
		if want[i].Count != want[j].Count {
			return want[i].Count > want[j].Count
		}
		return want[i].Word < want[j].Word
	})
	text := strings.Join(tokens, ", ")
	for _, n := range []int{1, 10, 57, distinct, distinct + 50} {
		sameResult(t, TopN(text, n, nil), want[:min(n, len(want))])
	}
}
