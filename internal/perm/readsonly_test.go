package perm

import "testing"

// ReadsOnly tells a line that only reads and prints from one that may change something. The read-only lines are the shapes a
// worker uses to read code in one call (cd, echo banners, cat, sed -n, grep, find, sort | head, 2>/dev/null, || echo, loops); the
// others write a file, run a program the allowlist does not know, or cannot be read with confidence.
func TestReadsOnly(t *testing.T) {
	reads := []string{
		`cat -n src/pages/Incidents.tsx`,
		`cd /home/u/proj/web && echo "===== Incidents.tsx =====" && cat -n src/pages/Incidents.tsx`,
		`cd web && echo "=== types (64-115) ===" && sed -n '64,115p' src/lib/types.ts && echo "=== handlers ===" && grep -n "incident" src/test/handlers.ts`,
		`cd web && echo "=== a ==="; cat src/a.ts; echo "=== b ==="; sed -n '1,140p' src/b.ts`,
		`grep -o "\-\-rw-[a-z0-9-]*" src/styles/tokens.css | sort -u | head -80`,
		`find web -type f -not -path '*/node_modules/*' 2>/dev/null | sort || echo "web still empty"`,
		`ls web/src 2>/dev/null && ls web/src/* 2>/dev/null | head -60`,
		`for f in Badge Button Banner; do echo "== $f =="; grep -n "export" src/components/ui/$f.tsx; done`,
		`cat -n src/data/queries.ts | sed -n '60,170p'`,
		`git status --short && git log --oneline -5 && git diff HEAD~1 -- internal/swarm`,
		`wc -l internal/swarm/*.go 2>&1 | tail -3`,
		`pwd && ls -la`,
		`cat <<'EOF'
notes
EOF`,
		`X=1 cat a.txt`,
	}
	for _, c := range reads {
		if !ReadsOnly(c) {
			t.Errorf("only reads, judged as more: %s", c)
		}
	}
	more := []string{
		``,
		`   `,
		`go test ./...`,
		`npx tsc --noEmit -p tsconfig.json 2>&1 | grep -E "pages/Alerts"`,
		`npm test`,
		`sed -i 's/a/b/' x.go`,
		`cat a > b`,
		`echo hi >> notes.md`,
		`cat a | tee b`,
		`find . -name '*.orig' -delete`,
		`find . -exec rm {} \;`,
		`mkdir -p web/src && cat src/a.ts`,
		`cd web && npm install`,
		`git checkout -- x.go`,
		`git commit -m wip`,
		`python3 -c 'print(1)'`,
		`bash -c 'rm -rf x'`,
		`sh -c 'cat a > b'`,
		`echo $(rm -f x)`,
		`cat a.txt; rm b.txt`,
		`PATH=/tmp/bin:$PATH cat a`,
		`GIT_EXTERNAL_DIFF=/tmp/x git diff`,
		`$CMD a b`,
		`./scripts/look.sh out.png`,
		`echo "unterminated`,
		`sort -o out.txt in.txt`,
		`cat a >&b.txt`,
	}
	for _, c := range more {
		if ReadsOnly(c) {
			t.Errorf("may change something, judged read-only: %q", c)
		}
	}
}
