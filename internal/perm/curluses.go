package perm

import "strings"

// What curl reads and writes.
//
// curl's operands are URLs, and most of its option values are data, headers, methods or numbers, not files: judged as an unlisted
// command, every word was a path it might write, so a smoke test's JSON body that held a variable (-d "{\"category_id\":$CID}") was a
// write to an unknown place and stopped a bypass session for the person. curlUses names the files curl does touch: what it writes
// (-o, -c, -D, --trace, and the file -O names after the URL, in --output-dir when one is given) and what it reads (@file data and
// headers, -T, certificates, a cookie file, the user's .netrc, a file: URL). What it cannot name leaves the command unlisted: an option
// it does not know, a config file (-K holds options of its own), a name the server picks (-J), a URL whose scheme or file: path is only
// known when the command runs, an -o name filled from a URL glob (#1).

// curlBare are curl options without a value that touch no file.
var curlBare = curlSet("-s", "--silent", "-S", "--show-error", "-f", "--fail", "--fail-with-body", "-i", "--include", "-I", "--head",
	"-v", "--verbose", "-N", "--no-buffer", "-G", "--get", "-g", "--globoff", "--compressed", "--http1.0", "--http1.1", "--http2",
	"-k", "--insecure", "--no-progress-meter", "-L", "--location", "-#", "--progress-bar", "-q", "--disable", "--path-as-is", "--raw",
	"-Z", "--parallel", "--ipv4", "-4", "--ipv6", "-6", "--tcp-nodelay", "--no-keepalive", "--location-trusted")

// curlData are the options whose value is data, or a file to read when it starts with "@" (or "<" in a form field).
var curlData = curlSet("-d", "--data", "--data-binary", "--data-ascii", "--data-urlencode", "--json", "-F", "--form", "--form-string")

// curlPlain are the options whose value names no file (-H reads one when it starts with "@"; --url is a URL).
var curlPlain = curlSet("-X", "--request", "-H", "--header", "--data-raw", "-m", "--max-time", "--connect-timeout", "--retry",
	"--retry-delay", "--retry-max-time", "-A", "--user-agent", "-u", "--user", "-e", "--referer", "-x", "--proxy", "--resolve",
	"-r", "--range", "--limit-rate", "-w", "--write-out", "--max-redirs", "-z", "--time-cond", "--url", "--connect-to")

// curlReads are the options whose value is a file curl reads; curlWrites, one it writes.
var (
	curlReads  = curlSet("-T", "--upload-file", "--cacert", "--capath", "-E", "--cert", "--key", "--netrc-file")
	curlWrites = curlSet("-c", "--cookie-jar", "-D", "--dump-header", "--trace", "--trace-ascii", "--stderr")
)

// curlUses returns the files a curl command line reads and writes, and false when it cannot name them (see above). known is a word
// with the variables the check knows put in, once for each directory the command may run in; when part of it is only known as the
// command runs, it returns the known beginnings and false.
func curlUses(args []string, known func(string) ([]string, bool)) ([]pathUse, bool) {
	var (
		uses       []pathUse
		urls, outs []string
		outDir     string
		remoteName bool
	)
	read := func(p string) { uses = append(uses, pathUse{raw: p}) }
	write := func(p string) {
		if p != "-" && p != "/dev/null" {
			uses = append(uses, pathUse{raw: p, write: true})
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			urls = append(urls, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			urls = append(urls, a)
			continue
		}
		name, val, inline := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") {
			inline = false
			name = a
		}
		switch {
		case curlBare[a] || shortCluster(a, "sSfiIvNGgkLqZ46"):
			continue
		case a == "-O" || a == "--remote-name" || a == "--remote-name-all":
			remoteName = true // the file is named by the URL
			continue
		case a == "-n" || a == "--netrc" || a == "--netrc-optional":
			read("~/.netrc")
			continue
		}
		if !curlData[name] && !curlPlain[name] && !curlReads[name] && !curlWrites[name] && name != "-b" && name != "--cookie" &&
			name != "-o" && name != "--output" && name != "--output-dir" {
			return nil, false // -K and -J are not known here either: what they write is not on the command line
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, false
			}
			i++
			val = args[i]
		}
		switch {
		case name == "-o" || name == "--output":
			outs = append(outs, val)
		case name == "--output-dir":
			outDir = val
		case name == "--url":
			if strings.HasPrefix(val, "@") {
				return nil, false // a file of URLs
			}
			urls = append(urls, val)
		case curlReads[name]:
			read(val)
		case curlWrites[name]:
			write(val)
		case name == "-b" || name == "--cookie":
			if !strings.Contains(val, "=") {
				read(val) // a cookie file; "name=value" is a cookie
			}
		case name == "-H" || name == "--header" || curlData[name]:
			// The value is judged as curl sees it, after the shell's expansions: a
			// variable that expands to "@/path" makes curl read and send that file.
			vals, whole := known(val)
			if !whole {
				if expansionMayNameFile(name, val) {
					return nil, false
				}
				vals = []string{val}
			}
			for _, v := range vals {
				if name == "-H" || name == "--header" {
					if strings.HasPrefix(v, "@") {
						read(v[1:]) // a file of headers
					}
					continue
				}
				for _, f := range curlDataFiles(name, v) {
					read(f)
				}
			}
		case name == "-w" || name == "--write-out":
			// %output{file} and %output{>>file} write the rest of the output to a file
			// (curl 8.3); a format read from a file may hold them too, unseen here.
			if strings.HasPrefix(val, "@") {
				return nil, false
			}
			files, ok := curlWriteOutFiles(val)
			if !ok {
				return nil, false
			}
			for _, f := range files {
				write(f)
			}
		}
	}
	for _, u := range urls {
		ws, whole := known(u)
		for _, w := range ws {
			file, path, ok := curlFileURL(w, whole)
			if !ok {
				return nil, false
			}
			if file {
				uses = append(uses, pathUse{raw: path, both: true}) // read, or written by an upload (-T) to it
			}
			if remoteName {
				n, ok := curlRemoteName(w, whole)
				if !ok {
					return nil, false
				}
				outs = append(outs, n)
			}
		}
	}
	for _, o := range outs {
		if curlGlobRef(o) {
			return nil, false // #1 is filled in from a URL glob
		}
		if outDir != "" && o != "-" && o != "/dev/null" {
			o = strings.TrimSuffix(outDir, "/") + "/" + strings.TrimLeft(o, "/") // curl puts every output name below --output-dir
		}
		write(o)
	}
	return uses, true
}

// curlFileURL reports whether a URL (w, whole when no part of it is only known as the command runs) is a file: URL and the local file it
// names; ok is false when that cannot be told: its scheme is not known yet, or a file: URL names another host, holds %-escapes or a glob,
// or is not known in full.
func curlFileURL(w string, whole bool) (file bool, path string, ok bool) {
	k := strings.IndexFunc(w, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-')
	})
	if k < 0 {
		return false, "", whole // all of it could still be a scheme ("$U", "file$X") when part is not known
	}
	if w[k] != ':' || !strings.EqualFold(w[:k], "file") {
		return false, "", true
	}
	rest, isURL := strings.CutPrefix(w[k+1:], "//")
	if !whole || !isURL {
		return true, "", false
	}
	host, p, _ := strings.Cut(rest, "/")
	if host != "" && !strings.EqualFold(host, "localhost") && host != "127.0.0.1" {
		return true, "", false
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if strings.ContainsAny(p, "%{}[]") {
		return true, "", false
	}
	return true, "/" + p, true
}

// curlRemoteName is the name -O saves a URL under: the last part of its path, without query or fragment. ok is false when there is none
// or it is not known in full.
func curlRemoteName(w string, whole bool) (string, bool) {
	if !whole {
		return "", false
	}
	if _, after, ok := strings.Cut(w, "://"); ok {
		w = after
	}
	if i := strings.IndexAny(w, "?#"); i >= 0 {
		w = w[:i]
	}
	i := strings.LastIndexByte(w, '/')
	if i < 0 {
		return "", false
	}
	n := w[i+1:]
	if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "%{}[]\\") {
		return "", false
	}
	return n, true
}

// curlGlobRef reports whether an output name holds #N, which curl replaces with the part a URL glob matched.
func curlGlobRef(o string) bool {
	for i := 0; i+1 < len(o); i++ {
		if o[i] == '#' && o[i+1] >= '0' && o[i+1] <= '9' {
			return true
		}
	}
	return false
}

// curlDataFiles are the files a data or form value reads: @file (a form field's name=@file or name=<file, and its headers=@file), or
// none.
func curlDataFiles(name, val string) []string {
	switch name {
	case "-F", "--form":
		_, v, ok := strings.Cut(val, "=")
		if !ok {
			return nil
		}
		var out []string
		parts := strings.Split(v, ";")
		if strings.HasPrefix(parts[0], "@") || strings.HasPrefix(parts[0], "<") {
			out = append(out, parts[0][1:])
		}
		for _, p := range parts[1:] {
			if h, ok := strings.CutPrefix(strings.TrimSpace(p), "headers="); ok && strings.HasPrefix(h, "@") {
				out = append(out, h[1:])
			}
		}
		return out
	case "--form-string":
		return nil
	case "--data-urlencode":
		at, eq := strings.IndexByte(val, '@'), strings.IndexByte(val, '=')
		if at >= 0 && (eq < 0 || at < eq) {
			return []string{val[at+1:]}
		}
		return nil
	}
	if strings.HasPrefix(val, "@") {
		return []string{val[1:]}
	}
	return nil
}

// curlSet makes a set of option names.
func curlSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// expansionMayNameFile reports whether the shell's expansion of the unknown part
// of an option value could make curl read a file: when the value, or for a form
// field or --data-urlencode the part after its name, begins with an expansion
// (which may produce "@file" or "<file"), or a --data-urlencode value holds an
// expansion before its "@".
func expansionMayNameFile(name, val string) bool {
	start := val
	switch name {
	case "-F", "--form", "--data-urlencode":
		if i := strings.IndexByte(val, '='); i >= 0 {
			start = val[i+1:]
		}
		if name == "--data-urlencode" && strings.ContainsAny(val, "$`") {
			return true // name@file: the "@" may come from the expansion
		}
	case "--form-string":
		return false
	}
	return strings.HasPrefix(start, "$") || strings.HasPrefix(start, "`") || strings.HasPrefix(start, `"$`)
}

// curlWriteOutFiles returns the files a --write-out format writes through
// %output{...}, and false when one of them is only known as curl runs.
func curlWriteOutFiles(format string) ([]string, bool) {
	var out []string
	for {
		i := strings.Index(format, "%output{")
		if i < 0 {
			return out, true
		}
		rest := format[i+len("%output{"):]
		j := strings.IndexByte(rest, '}')
		if j < 0 {
			return nil, false
		}
		f := strings.TrimPrefix(rest[:j], ">>")
		if f == "" || strings.ContainsAny(f, "$`%") {
			return nil, false
		}
		out = append(out, f)
		format = rest[j+1:]
	}
}
