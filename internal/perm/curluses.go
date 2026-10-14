package perm

import "strings"

// What curl reads and writes.
//
// curl's operands are URLs, and most of its option values are data, headers, methods or numbers, not files: judged as an unlisted
// command, every word was a path it might write, so a smoke test's JSON body that held a variable (-d "{\"category_id\":$CID}") was a
// write to an unknown place and stopped a bypass session for the person. curlUses names the files curl does touch: what it writes
// (-o, -c, -D, --trace, --output-dir, -O into the current directory) and what it reads (@file data, -T, -K, certificates, a cookie
// file, the user's .netrc). An option it does not know leaves the command unlisted.

// curlBare are curl options without a value that touch no file.
var curlBare = curlSet("-s", "--silent", "-S", "--show-error", "-f", "--fail", "--fail-with-body", "-i", "--include", "-I", "--head",
	"-v", "--verbose", "-N", "--no-buffer", "-G", "--get", "-g", "--globoff", "--compressed", "--http1.0", "--http1.1", "--http2",
	"-k", "--insecure", "--no-progress-meter", "-L", "--location", "-#", "--progress-bar", "-q", "--disable", "--path-as-is", "--raw",
	"-Z", "--parallel", "--ipv4", "-4", "--ipv6", "-6", "--tcp-nodelay", "--no-keepalive", "--location-trusted")

// curlData are the options whose value is data, or a file to read when it starts with "@" (or "<" in a form field).
var curlData = curlSet("-d", "--data", "--data-binary", "--data-ascii", "--data-urlencode", "--json", "-F", "--form", "--form-string")

// curlPlain are the options whose value names no file.
var curlPlain = curlSet("-X", "--request", "-H", "--header", "--data-raw", "-m", "--max-time", "--connect-timeout", "--retry",
	"--retry-delay", "--retry-max-time", "-A", "--user-agent", "-u", "--user", "-e", "--referer", "-x", "--proxy", "--resolve",
	"-r", "--range", "--limit-rate", "-w", "--write-out", "--max-redirs", "-z", "--time-cond", "--url", "--connect-to")

// curlReads are the options whose value is a file curl reads; curlWrites, one it writes.
var (
	curlReads  = curlSet("-T", "--upload-file", "-K", "--config", "--cacert", "--capath", "-E", "--cert", "--key", "--netrc-file")
	curlWrites = curlSet("-o", "--output", "-c", "--cookie-jar", "-D", "--dump-header", "--trace", "--trace-ascii", "--output-dir", "--stderr")
)

// curlUses returns the files a curl command line reads and writes, and false when an option is not one curl is known by here.
func curlUses(args []string) ([]pathUse, bool) {
	var uses []pathUse
	read := func(p string) { uses = append(uses, pathUse{raw: p}) }
	write := func(p string) {
		if p != "-" && p != "/dev/null" {
			uses = append(uses, pathUse{raw: p, write: true})
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return uses, true // URLs follow
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			continue // a URL
		}
		name, val, inline := strings.Cut(a, "=")
		if !strings.HasPrefix(a, "--") {
			inline = false
			name = a
		}
		switch {
		case curlBare[a] || shortCluster(a, "sSfiIvNGgkLqZ46"):
			continue
		case a == "-O" || a == "--remote-name" || a == "--remote-name-all" || a == "-J" || a == "--remote-header-name":
			write(".") // the file is named by the server or the URL, in the current directory
			continue
		case a == "-n" || a == "--netrc" || a == "--netrc-optional":
			read("~/.netrc")
			continue
		}
		if !curlData[name] && !curlPlain[name] && !curlReads[name] && !curlWrites[name] && name != "-b" && name != "--cookie" {
			return nil, false
		}
		if !inline {
			if i+1 >= len(args) {
				return nil, false
			}
			i++
			val = args[i]
		}
		switch {
		case curlReads[name]:
			read(val)
		case curlWrites[name]:
			write(val)
		case name == "-b" || name == "--cookie":
			if !strings.Contains(val, "=") {
				read(val) // a cookie file; "name=value" is a cookie
			}
		case curlData[name]:
			if f, ok := curlDataFile(name, val); ok {
				read(f)
			}
		case name == "-w" || name == "--write-out":
			if strings.HasPrefix(val, "@") {
				read(val[1:])
			}
		}
	}
	return uses, true
}

// curlDataFile is the file a data or form value reads: @file (a form field's name=@file or name=<file), or none.
func curlDataFile(name, val string) (string, bool) {
	switch name {
	case "-F", "--form":
		_, v, ok := strings.Cut(val, "=")
		if ok && (strings.HasPrefix(v, "@") || strings.HasPrefix(v, "<")) {
			f, _, _ := strings.Cut(v[1:], ";")
			return f, true
		}
		return "", false
	case "--form-string":
		return "", false
	case "--data-urlencode":
		at, eq := strings.IndexByte(val, '@'), strings.IndexByte(val, '=')
		if at >= 0 && (eq < 0 || at < eq) {
			return val[at+1:], true
		}
		return "", false
	}
	if strings.HasPrefix(val, "@") {
		return val[1:], true
	}
	return "", false
}

// curlSet makes a set of option names.
func curlSet(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
