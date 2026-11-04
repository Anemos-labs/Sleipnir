package gateway

import "testing"

func TestTakesImages(t *testing.T) {
	for modality, want := range map[string]bool{
		"":                         true, // a catalogue that does not say
		"text+image->text":         true,
		"text+image+video->text":   true,
		"text->text":               false,
		"text->decisions":          false,
		"Text+Image->Text":         true,
		"something unconventional": true,
	} {
		if got := (Entry{Modality: modality}).TakesImages(); got != want {
			t.Errorf("%q: %v, want %v", modality, got, want)
		}
	}
}
