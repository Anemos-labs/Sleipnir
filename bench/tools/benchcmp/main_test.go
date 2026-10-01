package main

import (
	"bytes"
	"strings"
	"testing"
)

func run(t *testing.T, lines ...string) map[string]*sample {
	t.Helper()
	m, err := parse(strings.NewReader(strings.Join(lines, "\n")))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseTakesTheBenchmarkLinesAndTheirRepeats(t *testing.T) {
	m := run(t,
		"goos: linux",
		"BenchmarkRender/anthropic/inline-2   \t     200\t     82281 ns/op\t   18912 B/op\t      69 allocs/op",
		"BenchmarkRender/anthropic/inline-2   \t     200\t     83000 ns/op\t   18912 B/op\t      69 allocs/op",
		"BenchmarkRender/anthropic/inline-8   \t     200\t     80000 ns/op\t   18912 B/op\t      69 allocs/op",
		"BenchmarkCanonical-2 100 3036 ns/op 13.50 MB/s 1576 B/op 21 allocs/op",
		"PASS", "ok  \tgithub.com/x/y\t0.4s", "Benchmarkish text that is not a result", "BenchmarkNoNumbers-2 abc",
	)
	if len(m) != 2 {
		t.Fatalf("%d benchmarks, want 2: %v", len(m), m)
	}
	if s := m["BenchmarkRender/anthropic/inline"]; s == nil || len(s.ns) != 3 || len(s.allocs) != 3 {
		t.Errorf("the GOMAXPROCS suffix is the machine's and the three runs are one benchmark: %+v", s)
	}
	if s := m["BenchmarkCanonical"]; s == nil || s.allocs[0] != 21 || s.bytes[0] != 1576 || s.ns[0] != 3036 {
		t.Errorf("a custom metric (MB/s) between the standard ones must not shift them: %+v", s)
	}
}

func TestOnlyARealSlowdownIsARegression(t *testing.T) {
	line := func(name string, ns, allocs int) string {
		return "Benchmark" + name + "-2 100 " + itoa(ns) + " ns/op 1000 B/op " + itoa(allocs) + " allocs/op"
	}
	old := run(t, line("Steady", 1000, 10), line("Steady", 1010, 10), line("Steady", 990, 10),
		line("Noisy", 1000, 10), line("Noisy", 1400, 10), line("Noisy", 1100, 10),
		line("Slower", 1000, 10), line("Slower", 1010, 10), line("Slower", 990, 10),
		line("Allocates", 1000, 10), line("Allocates", 1000, 10), line("Allocates", 1000, 10),
		line("Faster", 1000, 10), line("Faster", 1010, 10), line("Faster", 990, 10),
		line("Gone", 1, 1))
	nw := run(t, line("Steady", 1020, 10), line("Steady", 1005, 10), line("Steady", 1015, 10),
		line("Noisy", 1300, 10), line("Noisy", 1250, 10), line("Noisy", 1500, 10), // 25% above the median but inside the old range: noise
		line("Slower", 1300, 10), line("Slower", 1310, 10), line("Slower", 1290, 10),
		line("Allocates", 1000, 14), line("Allocates", 1000, 14), line("Allocates", 1000, 14),
		line("Faster", 600, 10), line("Faster", 610, 10), line("Faster", 590, 10),
		line("New", 5, 1))
	rows, onlyOld, onlyNew := compare(old, nw, 15)
	got := map[string]string{}
	for _, r := range rows {
		switch {
		case r.slower:
			got[r.name] = "slower"
		case r.moreAllocs:
			got[r.name] = "allocs"
		case r.faster:
			got[r.name] = "faster"
		default:
			got[r.name] = "same"
		}
	}
	for name, want := range map[string]string{"BenchmarkSteady": "same", "BenchmarkNoisy": "same", "BenchmarkSlower": "slower", "BenchmarkAllocates": "allocs", "BenchmarkFaster": "faster"} {
		if got[name] != want {
			t.Errorf("%s: %s, want %s", name, got[name], want)
		}
	}
	if len(onlyOld) != 1 || onlyOld[0] != "BenchmarkGone" || len(onlyNew) != 1 || onlyNew[0] != "BenchmarkNew" {
		t.Errorf("only in old %v, only in new %v", onlyOld, onlyNew)
	}
	var out bytes.Buffer
	if n := report(&out, rows, onlyOld, onlyNew); n != 2 {
		t.Errorf("%d regressions reported, want the two (slower, allocates):\n%s", n, out.String())
	}
	if !strings.Contains(out.String(), "SLOWER") || !strings.Contains(out.String(), "MORE ALLOCATIONS") {
		t.Errorf("the report must say what regressed:\n%s", out.String())
	}
}

// A benchmark that allocated nothing and now allocates once has regressed, whatever the percentage says.
func TestAllocationsFromNothingAreARegression(t *testing.T) {
	old := run(t, "BenchmarkSnapshot-2 100 50 ns/op 0 B/op 0 allocs/op")
	nw := run(t, "BenchmarkSnapshot-2 100 50 ns/op 16 B/op 1 allocs/op")
	rows, _, _ := compare(old, nw, 15)
	if len(rows) != 1 || !rows[0].moreAllocs {
		t.Errorf("%+v", rows)
	}
}

func itoa(n int) string {
	var b []byte
	if n == 0 {
		return "0"
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
