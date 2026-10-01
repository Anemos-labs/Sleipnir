// Command benchcmp compares two runs of `go test -bench ... -benchmem -count=N` and says what got slower or allocates more.
//
//	go run ./bench/tools/benchcmp [-threshold 15] OLD.txt NEW.txt
//
// Each file is the output of one run; a benchmark that appears several times in it (-count) is summarised by its median, and its
// range (the fastest and the slowest of the runs) says how much to trust it. A benchmark has regressed when its median time is more
// than the threshold (percent) above the old one AND the two ranges do not overlap, so noise on a shared machine is not reported;
// allocations per operation do not depend on the machine, so they are compared exactly (more than the threshold above the old, and
// at least one more). The exit status is 1 when anything regressed, 2 for a usage error.
//
// It is the standard library and nothing else, in the spirit of benchstat without its statistics: the numbers of a loaded machine
// are a hint, and the allocation gates (allocs_gate_test.go in a package) are what fail a build.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

type sample struct {
	ns, bytes, allocs []float64
}

type summary struct {
	name          string
	n             int
	median        float64
	min, max      float64
	allocs, bytes float64
	hasMem, hasNs bool
}

// parse reads the benchmark lines of go test output.
func parse(r io.Reader) (map[string]*sample, error) {
	out := map[string]*sample{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || !strings.HasPrefix(f[0], "Benchmark") {
			continue
		}
		if _, err := strconv.Atoi(f[1]); err != nil {
			continue
		}
		name := f[0]
		if i := strings.LastIndexByte(name, '-'); i > 0 { // the -GOMAXPROCS suffix is the machine's, not the benchmark's
			if _, err := strconv.Atoi(name[i+1:]); err == nil {
				name = name[:i]
			}
		}
		s := out[name]
		if s == nil {
			s = &sample{}
			out[name] = s
		}
		for i := 2; i+1 < len(f); i += 2 {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				continue
			}
			switch f[i+1] {
			case "ns/op":
				s.ns = append(s.ns, v)
			case "B/op":
				s.bytes = append(s.bytes, v)
			case "allocs/op":
				s.allocs = append(s.allocs, v)
			}
		}
	}
	return out, sc.Err()
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	if n := len(c); n%2 == 1 {
		return c[n/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

func summarise(name string, s *sample) summary {
	r := summary{name: name, n: len(s.ns), hasNs: len(s.ns) > 0, hasMem: len(s.allocs) > 0}
	if len(s.ns) > 0 {
		r.median = median(s.ns)
		r.min, r.max = s.ns[0], s.ns[0]
		for _, v := range s.ns {
			r.min, r.max = min(r.min, v), max(r.max, v)
		}
	}
	r.allocs, r.bytes = median(s.allocs), median(s.bytes)
	return r
}

type row struct {
	name                string
	old, new            summary
	timePct, allocsPct  float64
	slower, moreAllocs  bool
	faster, fewerAllocs bool
}

func compare(old, nw map[string]*sample, threshold float64) (rows []row, onlyOld, onlyNew []string) {
	for name, o := range old {
		n, ok := nw[name]
		if !ok {
			onlyOld = append(onlyOld, name)
			continue
		}
		r := row{name: name, old: summarise(name, o), new: summarise(name, n)}
		if r.old.hasNs && r.new.hasNs && r.old.median > 0 {
			r.timePct = 100 * (r.new.median - r.old.median) / r.old.median
			disjoint := r.new.min > r.old.max
			r.slower = r.timePct > threshold && disjoint
			r.faster = r.timePct < -threshold && r.new.max < r.old.min
		}
		if r.old.hasMem && r.new.hasMem {
			d := r.new.allocs - r.old.allocs
			if r.old.allocs > 0 {
				r.allocsPct = 100 * d / r.old.allocs
			}
			r.moreAllocs = d >= 1 && (r.old.allocs == 0 || r.allocsPct > threshold)
			r.fewerAllocs = d <= -1 && r.allocsPct < -threshold
		}
		rows = append(rows, r)
	}
	for name := range nw {
		if _, ok := old[name]; !ok {
			onlyNew = append(onlyNew, name)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	sort.Strings(onlyOld)
	sort.Strings(onlyNew)
	return rows, onlyOld, onlyNew
}

func human(ns float64) string {
	switch {
	case ns >= 1e9:
		return fmt.Sprintf("%.2f s", ns/1e9)
	case ns >= 1e6:
		return fmt.Sprintf("%.2f ms", ns/1e6)
	case ns >= 1e3:
		return fmt.Sprintf("%.2f µs", ns/1e3)
	}
	return fmt.Sprintf("%.0f ns", ns)
}

func report(w io.Writer, rows []row, onlyOld, onlyNew []string) (regressed int) {
	width := 0
	for _, r := range rows {
		width = max(width, len(r.name))
	}
	fmt.Fprintf(w, "%-*s  %12s  %12s  %8s  %9s  %9s  %s\n", width, "benchmark", "old", "new", "time", "allocs", "allocs", "")
	for _, r := range rows {
		verdict := ""
		switch {
		case r.slower && r.moreAllocs:
			verdict, regressed = "SLOWER, MORE ALLOCATIONS", regressed+1
		case r.slower:
			verdict, regressed = "SLOWER", regressed+1
		case r.moreAllocs:
			verdict, regressed = "MORE ALLOCATIONS", regressed+1
		case r.faster:
			verdict = "faster"
		case r.fewerAllocs:
			verdict = "fewer allocations"
		}
		fmt.Fprintf(w, "%-*s  %12s  %12s  %+7.1f%%  %4.0f→%-4.0f  %+7.1f%%  %s\n", width, r.name, human(r.old.median), human(r.new.median), r.timePct,
			r.old.allocs, r.new.allocs, r.allocsPct, verdict)
	}
	for _, n := range onlyOld {
		fmt.Fprintf(w, "only in the old run: %s\n", n)
	}
	for _, n := range onlyNew {
		fmt.Fprintf(w, "only in the new run: %s\n", n)
	}
	return regressed
}

func main() {
	threshold := flag.Float64("threshold", 15, "percent above the old median (time, with ranges that do not overlap; allocations, exactly) that is a regression")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: benchcmp [-threshold PERCENT] OLD.txt NEW.txt")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 || *threshold < 0 {
		flag.Usage()
		os.Exit(2)
	}
	var runs [2]map[string]*sample
	for i, p := range flag.Args() {
		f, err := os.Open(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "benchcmp:", err)
			os.Exit(2)
		}
		runs[i], err = parse(f)
		f.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "benchcmp:", p+":", err)
			os.Exit(2)
		}
		if len(runs[i]) == 0 {
			fmt.Fprintln(os.Stderr, "benchcmp:", p, "holds no benchmark lines (go test -bench . -benchmem output)")
			os.Exit(2)
		}
	}
	rows, onlyOld, onlyNew := compare(runs[0], runs[1], *threshold)
	if n := report(os.Stdout, rows, onlyOld, onlyNew); n > 0 {
		fmt.Fprintf(os.Stderr, "benchcmp: %d of %d benchmarks regressed (more than %.0f%%)\n", n, len(rows), *threshold)
		os.Exit(1)
	}
}
