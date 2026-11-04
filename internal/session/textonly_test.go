package session

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider/gateway"
)

// Only the entry of the exact id says a model takes text only: a normalised match may be another vendor's model.
func TestTakesImagesNeedsTheExactID(t *testing.T) {
	es := []gateway.Entry{{Model: cost.Model{ID: "other/model-x-20250101"}, Modality: "text->text"}}
	if !takesImages(es, "vendor/model-x") {
		t.Fatal("a normalised match made a model text only")
	}
	es = append(es, gateway.Entry{Model: cost.Model{ID: "vendor/model-x"}, Modality: "text->text"})
	if takesImages(es, "vendor/model-x") {
		t.Fatal("the model's own entry says text only")
	}
	if !takesImages(nil, "vendor/model-x") {
		t.Fatal("no catalogue must leave it able to see")
	}
}
