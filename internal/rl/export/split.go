package export

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/rl"
)

// splitBin is one named split with its cumulative upper bound in [0,1].
type splitBin struct {
	name string
	upTo float64
}

// parseSplit parses "train:0.9,val:0.1". Weights are relative (they are
// normalised), names must be unique and non-empty.
func parseSplit(s string) ([]splitBin, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var bins []splitBin
	var weights []float64
	seen := map[string]bool{}
	total := 0.0
	for _, part := range strings.Split(s, ",") {
		name, w, ok := strings.Cut(strings.TrimSpace(part), ":")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("split %q: want name:weight pairs such as train:0.9,val:0.1", s)
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(w), 64)
		if err != nil || f <= 0 || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("split %q: weight of %q must be a positive number", s, name)
		}
		if seen[name] {
			return nil, fmt.Errorf("split %q: %q appears twice", s, name)
		}
		seen[name] = true
		bins = append(bins, splitBin{name: name})
		weights = append(weights, f)
		total += f
	}
	acc := 0.0
	for i := range bins {
		acc += weights[i] / total
		bins[i].upTo = acc
	}
	bins[len(bins)-1].upTo = 1
	return bins, nil
}

// splitOf assigns an episode to a split by repository, so no repository straddles
// train and validation. The assignment depends only on the seed and the
// repository (or, without one, the task id): it is stable across runs and across
// other episodes being present or not.
func (x *exporter) splitOf(ep *rl.Episode) string {
	if len(x.splits) == 0 {
		return ""
	}
	key := ep.Env.Repo
	if key == "" {
		key = ep.TaskID
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", x.o.Seed, key)))
	u := float64(binary.BigEndian.Uint64(h[:8])>>11) / float64(1<<53)
	for _, b := range x.splits {
		if u < b.upTo {
			return b.name
		}
	}
	return x.splits[len(x.splits)-1].name
}
