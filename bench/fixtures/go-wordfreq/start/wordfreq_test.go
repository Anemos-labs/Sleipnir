package wordfreq

import (
	"reflect"
	"testing"
)

// These are the worked examples from README.md.
func TestReadmeExamples(t *testing.T) {
	tests := []struct {
		name string
		text string
		n    int
		stop []string
		want []Count
	}{
		{
			name: "most frequent first",
			text: "the cat and the hat and the bat",
			n:    2,
			want: []Count{{"the", 3}, {"and", 2}},
		},
		{
			name: "ties are alphabetical",
			text: "b a c a b",
			n:    3,
			want: []Count{{"a", 2}, {"b", 2}, {"c", 1}},
		},
		{
			name: "unicode letters and case folding",
			text: "Élan élan ÉLAN Zèbre zèbre",
			n:    5,
			want: []Count{{"élan", 3}, {"zèbre", 2}},
		},
		{
			name: "stop words ignore case",
			text: "To be or not to be",
			n:    3,
			stop: []string{"TO", "or"},
			want: []Count{{"be", 2}, {"not", 1}},
		},
		{
			name: "separators and apostrophes",
			text: "Don't stop-believing, it's 2 late; rock'n'roll!",
			n:    10,
			want: []Count{{"believing", 1}, {"don't", 1}, {"it's", 1}, {"late", 1}, {"rock'n'roll", 1}, {"stop", 1}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TopN(tc.text, tc.n, tc.stop)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("TopN(%q, %d, %q)\n got  %v\n want %v", tc.text, tc.n, tc.stop, got, tc.want)
			}
		})
	}
}
