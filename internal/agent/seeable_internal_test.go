package agent

import (
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

// A model that takes text only gets a note where a tool returned an image, which its endpoint would refuse; a model that
// sees images gets the image.
func TestSeeableReplacesImagesForTextOnlyModels(t *testing.T) {
	img := core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "h"}
	blocks := []core.Block{core.Text("a"), img}
	a := &Agent{cfg: Config{Model: cost.Model{ID: "sees"}}}
	if got := a.seeable(blocks); len(got) != 2 || got[1].Kind != core.BlockImage {
		t.Fatalf("a model that sees images lost the image: %+v", got)
	}
	a.cfg.Model = cost.Model{ID: "blind", NoImages: true}
	got := a.seeable(blocks)
	if len(got) != 2 || got[1].Kind != core.BlockText || !strings.Contains(got[1].Text, "blind takes text only") {
		t.Fatalf("got %+v", got)
	}
	if blocks[1].Kind != core.BlockImage {
		t.Fatal("the tool's own blocks were changed")
	}
}
