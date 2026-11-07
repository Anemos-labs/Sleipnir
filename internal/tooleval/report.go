package tooleval

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Print writes the report for a reader: each variant's pass rate beside the size of its descriptions, then each case under
// each variant with the run it came from and, where it failed, the most common reason.
func Print(w io.Writer, r *Report) {
	fmt.Fprintf(w, "models: %s\n\n", strings.Join(r.Models, ", "))
	base := -1
	for i, v := range r.Variants {
		if v.Name == Current {
			base = i
		}
	}
	fmt.Fprintf(w, "%-20s %8s %10s %12s %8s\n", "variant", "pass", "rate", "tool bytes", "errors")
	for _, v := range r.Variants {
		delta := ""
		if base >= 0 && v.Name != Current {
			delta = fmt.Sprintf(" (%+d)", v.Bytes-r.Variants[base].Bytes)
		}
		fmt.Fprintf(w, "%-20s %4d/%-3d %9.0f%% %12d %8d%s\n", v.Name, v.Passed, v.Asked, 100*v.Rate(), v.Bytes, v.Errors, delta)
	}
	fmt.Fprintln(w)
	for _, c := range r.Cases {
		fmt.Fprintf(w, "%s", c.ID)
		if c.Source != "" {
			fmt.Fprintf(w, "  [%s]", c.Source)
		}
		fmt.Fprintln(w)
		for _, v := range r.Variants {
			fmt.Fprintf(w, "  %-18s %d/%d", v.Name, c.Passed[v.Name], c.Asked[v.Name])
			if why := c.Why[v.Name]; why != "" {
				fmt.Fprintf(w, "  %s", why)
			}
			fmt.Fprintln(w)
		}
	}
	var errs []string
	for _, s := range r.Samples {
		if s.Error != "" {
			errs = append(errs, fmt.Sprintf("%s %s %s #%d: %s", s.Model, s.Variant, s.Case, s.N, s.Error))
		}
	}
	if len(errs) > 0 {
		fmt.Fprintf(w, "\n%d samples got no answer:\n", len(errs))
		for i, e := range errs {
			if i == 5 {
				fmt.Fprintf(w, "  and %d more\n", len(errs)-5)
				break
			}
			fmt.Fprintf(w, "  %s\n", e)
		}
	}
	fmt.Fprintf(w, "\ncost: $%.4f\n", r.USD)
}

// PrintSizes writes the size of each tool under each variant, largest first: what every prompt pays for it.
func PrintSizes(w io.Writer, r *Report) {
	if len(r.Variants) == 0 {
		return
	}
	names := make([]string, 0, len(r.Variants[0].Tools))
	for n := range r.Variants[0].Tools {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := r.Variants[0].Tools[names[i]], r.Variants[0].Tools[names[j]]
		if a != b {
			return a > b
		}
		return names[i] < names[j]
	})
	fmt.Fprintf(w, "%-14s", "tool")
	for _, v := range r.Variants {
		fmt.Fprintf(w, " %12s", v.Name)
	}
	fmt.Fprintln(w)
	total := make([]int, len(r.Variants))
	for _, n := range names {
		fmt.Fprintf(w, "%-14s", n)
		for i, v := range r.Variants {
			fmt.Fprintf(w, " %12d", v.Tools[n])
			total[i] += v.Tools[n]
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "%-14s", "total")
	for _, t := range total {
		fmt.Fprintf(w, " %12d", t)
	}
	fmt.Fprintf(w, "\n(about %d tokens for the first variant at 4 bytes a token)\n", total[0]/4)
}
