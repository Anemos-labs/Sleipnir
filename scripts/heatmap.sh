#!/bin/sh
# A heatmap of the repository by when each file was last changed: what is hot (touched lately) and what has been forgotten.
#
#   scripts/heatmap.sh [--depth N] [--files N] [--html FILE] [--all] [--since-days N]
#
#   --depth N       group files by their first N path components (default 2: internal/agent, cmd/sleipnir, docs/media, ...)
#   --files N       also list the N coldest files (default 15; 0 for none)
#   --html FILE     write a self-contained page instead: one square per file, its area the file's size, its colour how recently it changed
#   --all           include generated and binary-ish files (docs/media, testdata, go.sum, lockfiles); by default they are left out
#   --since-days N  with the text report, list only directories whose newest change is older than N days (the forgotten ones)
#
# Heat is a file's rank among all files by date of last commit, not a number of days: a young repository has no file a year old, and a
# scale in days would paint it all one colour. Red is the coldest tenth, green the hottest. Dates are the last commit that touched
# the file (renames are followed), so a mass reformat makes everything hot: look at the files, not only the colours. Needs git and awk.
set -eu
LC_ALL=C
export LC_ALL
cd "$(dirname "$0")/.."

DEPTH=2 FILES=15 HTML= ALL=0 SINCE=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -h|--help) awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"; exit 0 ;;
    --depth) shift; DEPTH=${1:?} ;;
    --files) shift; FILES=${1:?} ;;
    --html) shift; HTML=${1:?} ;;
    --all) ALL=1 ;;
    --since-days) shift; SINCE=${1:?} ;;
    *) echo "heatmap: unknown option $1 (try --help)" >&2; exit 2 ;;
  esac
  shift
done
git rev-parse --is-shallow-repository 2>/dev/null | grep -q true && echo "heatmap: this clone is shallow: every file looks as old as its last fetched commit (git fetch --unshallow)" >&2

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# the last commit time of every path ever seen, newest first wins
git log --format='@%ct' --name-only --no-renames | awk '/^@/ { t = substr($0, 2); next } NF && !($0 in seen) { seen[$0] = t; print t "\t" $0 }' > "$tmp/last"

# the tracked files, with their size in lines
git ls-files -z | xargs -0 wc -l 2>/dev/null | awk '$2 != "total" { n = $1; $1 = ""; sub(/^ /, ""); print n "\t" $0 }' > "$tmp/loc"

# one row per file: time, lines, path; generated files out unless --all
awk -F'\t' -v all="$ALL" '
  NR == FNR { last[$2] = $1; next }
  { path = $2; if (!(path in last)) next
    if (!all && path ~ /(^docs\/media\/|\/testdata\/|(^|\/)go\.sum$|\.svg$|\.png$|\.gif$|\.lock$|\.golden$|\.jsonl$)/) next
    print last[path] "\t" ($1 < 1 ? 1 : $1) "\t" path }' "$tmp/last" "$tmp/loc" | sort -n > "$tmp/rows"

total=$(wc -l < "$tmp/rows")
[ "$total" -gt 0 ] || { echo "heatmap: no files" >&2; exit 1; }

# rank 0 (coldest) .. 1 (hottest) by order of last change; ties share the rank of the first
awk -F'\t' -v n="$total" '{ if ($1 != prev) { r = (NR - 1) / (n > 1 ? n - 1 : 1); prev = $1 } print $1 "\t" $2 "\t" r "\t" $3 }' "$tmp/rows" > "$tmp/ranked"

if [ -n "$HTML" ]; then
  awk -F'\t' -v depth="$DEPTH" '
    function dir(p,   a, n, i, d) { n = split(p, a, "/"); if (n == 1) return "(root)"; d = a[1]; for (i = 2; i <= depth && i < n; i++) d = d "/" a[i]; return d }
    function date(t) { return strftime("%m-%d %H:%M", t) }
    { d = dir($4); if (!(d in seen)) { seen[d] = 1; order[++k] = d } items[d] = items[d] sprintf("<div class=\"f\" style=\"width:%dpx;height:%dpx;background:hsl(%d,70%%,%d%%)\" title=\"%s&#10;%s, %d lines\"></div>", w($2), w($2), $3 * 120, 48, $4, date($1), $2); if (!(d in newest) || $1 > newest[d]) newest[d] = $1 }
    function w(l,   v) { v = int(8 + sqrt(l) * 2.2); return v > 120 ? 120 : v }
    END {
      print "<!doctype html><meta charset=utf-8><title>Heatmap</title><style>body{font:14px system-ui;margin:16px;background:#fff;color:#111}@media(prefers-color-scheme:dark){body{background:#111;color:#eee}}h1{font-size:18px}h2{font-size:13px;margin:14px 0 4px;font-weight:600}.g{display:flex;flex-wrap:wrap;gap:2px}.f{border-radius:2px}.k{display:inline-block;width:160px;height:10px;background:linear-gradient(90deg,hsl(0,70%,48%),hsl(60,70%,48%),hsl(120,70%,48%));vertical-align:middle}</style>"
      print "<h1>When each file last changed <small>(red: forgotten, green: recent; area: lines)</small></h1><p><span class=k></span> coldest to hottest. Hover a square for its path.</p>"
      for (i = 1; i <= k; i++) { d = order[i]; printf "<h2>%s <small>(newest change %s)</small></h2><div class=g>%s</div>\n", d, date(newest[d]), items[d] }
    }' "$tmp/ranked" > "$HTML"
  echo "wrote $HTML ($total files)"
  exit 0
fi

# the text report: one line per directory, coldest first
awk -F'\t' -v depth="$DEPTH" -v since="$SINCE" -v tty="$([ -t 1 ] && echo 1 || echo 0)" -v nowfile="$tmp/rows" '
  function dir(p,   a, n, i, d) { n = split(p, a, "/"); if (n == 1) return "(root)"; d = a[1]; for (i = 2; i <= depth && i < n; i++) d = d "/" a[i]; return d }
  function date(t) { return strftime("%m-%d %H:%M", t) }
  function bar(r,   i, s, c, pal) {
    split("196 202 208 214 220 226 190 154 118 46", pal, " ")
    c = int(r * 9.999) + 1
    if (tty) return sprintf("\033[48;5;%sm  \033[0m", pal[c]) ""
    return substr("..::--==##", c, 1)
  }
  { d = dir($4); n[d]++; loc[d] += $2; rank[d] += $3; if (!(d in hi) || $1 > hi[d]) hi[d] = $1; if (!(d in lo) || $1 < lo[d]) lo[d] = $1; if ($1 > all) all = $1 }
  END {
    for (d in n) { m = rank[d] / n[d]; if (since != "" && (all - hi[d]) / 86400 < since) continue
      printf "%.4f\t%s\t%d\t%d\t%s\t%s\n", m, d, n[d], loc[d], date(hi[d]), date(lo[d]) | "sort -n" }
  }' "$tmp/ranked" > "$tmp/dirs"
{
  printf '%-34s %6s %7s  %-11s  %-11s  %s\n' DIRECTORY FILES LINES NEWEST OLDEST HEAT
  awk -F'\t' -v tty="$([ -t 1 ] && echo 1 || echo 0)" '
    function bar(r,   c, pal) { split("196 202 208 214 220 226 190 154 118 46", pal, " "); c = int(r * 9.999) + 1
      if (tty) return sprintf("\033[48;5;%sm    \033[0m", pal[c]); return substr("..::--==##", c, 1) substr("..::--==##", c, 1) substr("..::--==##", c, 1) substr("..::--==##", c, 1) }
    { printf "%-34s %6d %7d  %-11s  %-11s  %s\n", $2, $3, $4, $5, $6, bar($1) }' "$tmp/dirs"
}
if [ "$FILES" -gt 0 ]; then
  printf '\nthe %s coldest files\n' "$FILES"
  head -n "$FILES" "$tmp/ranked" | awk -F'\t' '{ printf "  %s  %6d lines  %s\n", strftime("%m-%d %H:%M", $1), $2, $4 }'
fi
