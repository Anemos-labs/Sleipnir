package scan

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Proof files: a scan's proofs directory holds an index and one directory per run that reproduced the harm, each
// self-contained so it can be shared on its own: what the lead claims, how the run went, the script as it ran and what it
// printed. Runs that did not reproduce stay in the ledger and findings.json.
const (
	ProofsDir      = "proofs"
	ProofReadme    = "README.md"
	ProofScriptOut = "reproduce.sh"
	ProofOutput    = "output.txt"
	ProofRecord    = "proof.json"
)

// ProofDir is the directory of a lead's i-th run that reproduced (from 1), relative to the scan's directory.
func ProofDir(lead string, i int) string {
	return ProofsDir + "/" + strings.ToLower(lead) + "-" + fmt.Sprint(i)
}

// proofNumber is which reproduced run of f the i-th of its runs is (from 1), or 0 when that run did not reproduce. The ledger
// only adds runs, so the number of a run never changes.
func proofNumber(f *Finding, i int) int {
	if f.Proofs[i].Outcome != ProofReproduced {
		return 0
	}
	n := 0
	for _, p := range f.Proofs[:i+1] {
		if p.Outcome == ProofReproduced {
			n++
		}
	}
	return n
}

// WriteProofs writes the scan's reproduced runs from its ledger into its proofs directory, as the report gives them (no agent
// names). The ledger only ever adds reproductions, so rewriting them all keeps the directory in step with it; a scan whose
// proofs directory was left empty gets its proofs on the next report.
func WriteProofs(root string, r *Run) error {
	dir := RunDir(root, r.Slug)
	fs := Findings(r)
	var idx strings.Builder
	fmt.Fprintf(&idx, "# Proofs of scan %s\n\nEvery run here reproduced the harm. Each ran in a fresh copy of the code at commit `%s`, in a sandbox. Each directory holds the script, its output and a README that says what it shows and how to run it again.\n\n", r.Slug, r.Commit)
	n := 0
	for _, f := range fs {
		for i, p := range f.Proofs {
			k := proofNumber(&f, i)
			// the lead names a directory: one read back from a scan record is checked like one the scan made
			if k == 0 || !leadIDRE.MatchString(f.ID) {
				continue
			}
			rel := ProofDir(f.ID, k)
			files := map[string][]byte{
				ProofReadme:    []byte(proofReadme(r, &f, &p)),
				ProofScriptOut: []byte(p.Script),
				ProofOutput:    []byte(p.Output),
			}
			rec, err := json.MarshalIndent(struct {
				Scan     string `json:"scan"`
				Commit   string `json:"commit"`
				Lead     string `json:"lead"`
				Title    string `json:"title"`
				Severity string `json:"severity"`
				Status   string `json:"status"`
				Reproduction
			}{r.Slug, r.Commit, f.ID, f.Title, f.Severity, f.Status, p}, "", " ")
			if err != nil {
				return err
			}
			files[ProofRecord] = append(rec, '\n')
			for name, data := range files {
				if err := WriteAtomic(root, filepath.Join(dir, filepath.FromSlash(rel), name), data); err != nil {
					return err
				}
			}
			if n == 0 {
				idx.WriteString("| Proof | Lead | Severity | Outcome | Independent |\n|---|---|---|---|---|\n")
			}
			n++
			fmt.Fprintf(&idx, "| [%s](%s/%s) | %s: %s | %s | %s | %v |\n", strings.TrimPrefix(rel, ProofsDir+"/"), strings.TrimPrefix(rel, ProofsDir+"/"), ProofReadme,
				f.ID, mdCell(f.Title), f.Severity, p.Outcome, p.Independent)
		}
	}
	if n == 0 {
		// no index for a scan with no reproductions yet: the directory is made only when there is something in it
		return nil
	}
	return WriteAtomic(root, filepath.Join(dir, ProofsDir, ProofReadme), []byte(idx.String()))
}

func proofReadme(r *Run, f *Finding, p *Reproduction) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	w("# %s: %s\n\n", f.ID, oneLine(f.Title))
	w("- **Severity:** %s\n- **Status:** %s\n- **Category:** %s\n", f.Severity, f.Status, f.Category)
	w("- **Outcome:** %s (exit %d, app ready %v, target exercised %v, %ds)\n", p.Outcome, p.Exit, p.Ready, p.Exercised, p.Seconds)
	if p.Independent {
		w("- **Reproducer:** independent of the finding's filer\n")
	} else {
		w("- **Reproducer:** the filer's own; only an independent one proves the finding\n")
	}
	if p.Network {
		w("- **Network:** the sandbox let the script reach the network\n")
	}
	w("- **Commit:** `%s` (scan `%s`)\n", r.Commit, r.Slug)
	if p.At != "" {
		w("- **Run at:** %s\n", p.At)
	}
	w("\n## Claim\n\n%s\n\n", strings.TrimSpace(f.Claim))
	if len(f.Trace) > 0 {
		w("## Trace\n\n")
		for i, s := range f.Trace {
			w("%d. `%s:%d` %s\n", i+1, s.File, s.Line, oneLine(s.Note))
		}
		w("\n")
	}
	w("## Run it again\n\nIn a checkout of the repository, make a copy of the code at the commit, put this directory's %s in it and run it, in a sandbox (the script runs the code under test):\n\n", ProofScriptOut)
	writeFence(&b, "", fmt.Sprintf("git worktree add --detach /tmp/proof %s\ncp path/to/%s /tmp/proof/\ncd /tmp/proof && bash %s", r.Commit, ProofScriptOut, ProofScriptOut))
	w("The run reproduced the harm when it printed `%s` and `%s` and exited 0.\n\n", MarkerExercised, MarkerReproduced)
	w("## Output\n\nAs printed by the run (its end, when it was long); also in [%s](%s).\n\n", ProofOutput, ProofOutput)
	writeFence(&b, "", p.Output)
	return b.String()
}

func mdCell(s string) string { return strings.ReplaceAll(oneLine(s), "|", `\|`) }
