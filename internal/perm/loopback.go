package perm

import (
	"net/url"
	"strings"
)

// loopbackHosts are the names of this machine that a loopback rule (Bash(curl:loopback)) accepts, as url.URL.Hostname gives them.
var loopbackHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// curlFlags are the curl options without a value that a loopback request may carry: output and diagnostics, no redirect, no file.
var curlFlags = map[string]bool{
	"-s": true, "--silent": true, "-S": true, "--show-error": true, "-f": true, "--fail": true, "--fail-with-body": true,
	"-i": true, "--include": true, "-I": true, "--head": true, "-v": true, "--verbose": true, "-N": true, "--no-buffer": true,
	"-G": true, "--get": true, "-g": true, "--globoff": true, "--compressed": true, "--http1.1": true, "--http2": true,
	"-k": true, "--insecure": true, "--no-progress-meter": true,
}

// curlValues are the curl options that take a value which a loopback request may carry. A value of a data, header or write-out
// option that starts with "@" names a file to read, and is refused.
var curlValues = map[string]bool{
	"-X": true, "--request": true, "-H": true, "--header": true, "-d": true, "--data": true, "--data-raw": true,
	"--data-binary": true, "--data-urlencode": true, "--json": true, "-m": true, "--max-time": true, "--connect-timeout": true,
	"--retry": true, "--retry-delay": true, "--retry-max-time": true, "-w": true, "--write-out": true, "-A": true,
	"--user-agent": true, "-o": true, "--output": true,
}

// loopbackHTTP reports whether a curl or wget command line is an HTTP request to this machine and nothing more: every URL names a
// loopback host (loopbackHosts) over http or https, every option is one of a short list that neither reads nor writes a file (output
// goes to standard output or /dev/null), nothing follows a redirect to another host, and no word is left for the shell to expand. It is
// the smoke test of a server the agent started itself (curl -s http://127.0.0.1:8080/health). wget follows redirects unless told not
// to, so it qualifies only with --max-redirect=0.
func loopbackHTTP(prog string, args []string) bool {
	urls := 0
	noRedirect := prog == "curl"
	out := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.ContainsAny(a, "$`") {
			return false
		}
		if !strings.HasPrefix(a, "-") {
			if !loopbackURL(a) {
				return false
			}
			urls++
			continue
		}
		name, val, inline := strings.Cut(a, "=")
		switch prog {
		case "curl":
			if curlFlags[a] || shortCluster(a, "sSfiIvNGgk") {
				continue
			}
			if !curlValues[name] || inline && !strings.HasPrefix(name, "--") {
				return false
			}
			if !inline {
				if i+1 >= len(args) {
					return false
				}
				i++
				val = args[i]
			}
			if !curlValue(name, val) {
				return false
			}
		case "wget":
			switch {
			case a == "-q" || a == "--quiet" || a == "-S" || a == "--server-response" || a == "--spider" || a == "-nv" || a == "--no-verbose":
				if a == "--spider" {
					out = true
				}
			case a == "--max-redirect=0":
				noRedirect = true
			case a == "-O-" || a == "-O" && i+1 < len(args) && (args[i+1] == "-" || args[i+1] == "/dev/null") ||
				name == "--output-document" && (val == "-" || val == "/dev/null"):
				if a == "-O" {
					i++
				}
				out = true
			case name == "--timeout" || name == "--tries" || name == "--method" || name == "--header" || name == "--body-data":
				if !inline {
					return false
				}
			default:
				return false
			}
		default:
			return false
		}
	}
	if prog == "wget" && !out {
		return false // wget saves what it fetches to a file of the page's name
	}
	return urls > 0 && noRedirect
}

// curlValue reports whether the value of a curl option keeps the request a loopback read: output only to standard output or
// /dev/null, and no "@file" that curl would read.
func curlValue(name, val string) bool {
	switch name {
	case "-o", "--output":
		return val == "-" || val == "/dev/null"
	case "-X", "--request", "-m", "--max-time", "--connect-timeout", "--retry", "--retry-delay", "--retry-max-time", "-A", "--user-agent":
		return true
	}
	return !strings.HasPrefix(val, "@")
}

// shortCluster reports whether a is a cluster of short options ("-sS", "-fsS") that are all among letters.
func shortCluster(a, letters string) bool {
	if len(a) < 2 || a[0] != '-' || a[1] == '-' {
		return false
	}
	for _, c := range a[1:] {
		if !strings.ContainsRune(letters, c) {
			return false
		}
	}
	return true
}

// loopbackURL reports whether s is an http or https URL (or a scheme-less host:port/path, which curl reads as http) whose host is a
// loopback name.
func loopbackURL(s string) bool {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.Opaque != "" {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return loopbackHosts[strings.ToLower(u.Hostname())]
}
