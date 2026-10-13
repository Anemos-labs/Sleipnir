package perm

import "testing"

func TestLoopbackCurlFileForms(t *testing.T) {
	for _, tt := range []struct {
		option, value string
		want          bool
	}{
		{"--data-urlencode", "name@/etc/passwd", false},
		{"--data-urlencode", "@/etc/passwd", false},
		{"--data-urlencode", "name=user@example.com", true},
		{"--data-urlencode", "=user@example.com", true},
		{"--write-out", "%output{stolen.txt}%{http_code}", false},
		{"-w", "%output{>>stolen.txt}%{http_code}", false},
		{"-w", "%{http_code}", true},
	} {
		if got := loopbackHTTP("curl", []string{tt.option, tt.value, "http://localhost:8080"}); got != tt.want {
			t.Errorf("%s %q = %v, want %v", tt.option, tt.value, got, tt.want)
		}
	}
}

func TestInstallOptionsDoNotHideSubcommand(t *testing.T) {
	for _, tt := range []struct {
		program string
		args    []string
	}{
		{"npm", []string{"--prefix", ".", "install", "package"}},
		{"npm", []string{"--silent", "install", "package"}},
		{"go", []string{"-C", ".", "get", "example.com/x"}},
		{"python", []string{"-I", "-m", "pip", "install", "package"}},
	} {
		if !installs(tt.program, tt.args) {
			t.Errorf("missed install: %s %v", tt.program, tt.args)
		}
	}
}
