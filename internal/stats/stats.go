// Package stats holds the few statistics a benchmark report needs: an interval on a proportion, quantiles, the mean and the
// spread of a sample. Everything is deterministic and uses the standard library only.
package stats

import (
	"math"
	"sort"
)

// Z95 is the normal quantile of a two-sided 95% interval.
const Z95 = 1.959963984540054

// Wilson returns the Wilson score interval for k successes in n trials, z the normal quantile (Z95 for 95%). Unlike the
// textbook interval it is sane at the ends: 0 of 10 still allows a few percent, 10 of 10 is not certainty. With no
// trials nothing is known, and the interval is [0, 1].
func Wilson(k, n int, z float64) (lo, hi float64) {
	if n <= 0 {
		return 0, 1
	}
	k = max(0, min(k, n))
	p, nf, z2 := float64(k)/float64(n), float64(n), z*z
	denom := 1 + z2/nf
	center := (p + z2/(2*nf)) / denom
	half := z * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / denom
	return math.Max(0, center-half), math.Min(1, center+half)
}

// Mean is the arithmetic mean; 0 for no values.
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return Sum(xs) / float64(len(xs))
}

// Sum adds the values.
func Sum(xs []float64) float64 {
	var s float64
	for _, x := range xs {
		s += x
	}
	return s
}

// SD is the sample standard deviation (n-1 in the denominator); 0 for fewer than two values.
func SD(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := Mean(xs)
	var ss float64
	for _, x := range xs {
		ss += (x - m) * (x - m)
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

// Quantile is the q-quantile (0 to 1) by linear interpolation between the closest ranks (the usual "type 7"); the input
// is not modified. 0 for no values.
func Quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	q = math.Max(0, math.Min(1, q))
	h := q * float64(len(s)-1)
	lo, hi := int(math.Floor(h)), int(math.Ceil(h))
	return s[lo] + (s[hi]-s[lo])*(h-float64(lo))
}

// Median is the 0.5-quantile.
func Median(xs []float64) float64 { return Quantile(xs, 0.5) }
