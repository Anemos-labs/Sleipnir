package mdlite_test

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"example.com/mdlite"
)

// specRunCLI runs `go run ./cmd/mdlite args...` from the module root (the directory of this package) with the
// given standard input.
func specRunCLI(t *testing.T, stdin string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", append([]string{"run", "./cmd/mdlite"}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("go run ./cmd/mdlite: %v\n%s", err, errb.String())
		}
		return out.String(), errb.String(), ee.ExitCode()
	}
	return out.String(), errb.String(), 0
}

func TestSpecCLIDocument(t *testing.T) {
	in := "# Title\n\nIntro with *emphasis* and a [link](http://x.y).\nSecond line  \nafter a hard break.\n\n## Steps\n\n1. Install `mdlite`\n2. Run **it**\n\n- fast\n- small\n\n```sh\necho \"hi\" && ls\n```\n\nThe end.\n"
	want := "<h1>Title</h1>\n<p>Intro with <em>emphasis</em> and a <a href=\"http://x.y\">link</a>.\nSecond line<br>\nafter a hard break.</p>\n<h2>Steps</h2>\n<ol>\n<li>Install <code>mdlite</code></li>\n<li>Run <strong>it</strong></li>\n</ol>\n<ul>\n<li>fast</li>\n<li>small</li>\n</ul>\n<pre><code class=\"language-sh\">echo &#34;hi&#34; &amp;&amp; ls\n</code></pre>\n<p>The end.</p>\n"
	stdout, stderr, exit := specRunCLI(t, in)
	if exit != 0 {
		t.Fatalf("exit status %d, stderr:\n%s", exit, stderr)
	}
	if stdout != want {
		t.Errorf("stdout\n got: %q\nwant: %q", stdout, want)
	}
	if lib := mdlite.Render(in); stdout != lib {
		t.Errorf("the tool and the library disagree\n tool: %q\n  lib: %q", stdout, lib)
	}
}

func TestSpecCLIEmptyInput(t *testing.T) {
	stdout, stderr, exit := specRunCLI(t, "")
	if exit != 0 || stdout != "" {
		t.Errorf("empty input: exit %d, stdout %q, stderr %q; want exit 0 and no output", exit, stdout, stderr)
	}
}

func TestSpecCLIWithoutFinalNewlineAndNonASCII(t *testing.T) {
	stdout, stderr, exit := specRunCLI(t, "Café *naïve* <b>")
	want := "<p>Café <em>naïve</em> &lt;b&gt;</p>\n"
	if exit != 0 || stdout != want {
		t.Errorf("exit %d, stdout %q, stderr %q; want exit 0 and %q", exit, stdout, stderr, want)
	}
}

func TestSpecCLIReadsAllOfStandardInput(t *testing.T) {
	in := strings.Repeat("para *x*\n\n", 500)
	want := strings.Repeat("<p>para <em>x</em></p>\n", 500)
	stdout, stderr, exit := specRunCLI(t, in)
	if exit != 0 || stdout != want {
		t.Errorf("exit %d, stderr %q; stdout has %d bytes, want %d identical to the expectation", exit, stderr, len(stdout), len(want))
	}
}

func TestSpecCLIIgnoresArguments(t *testing.T) {
	stdout, _, exit := specRunCLI(t, "# Hi\n", "some-argument", "--flag")
	if exit != 0 || stdout != "<h1>Hi</h1>\n" {
		t.Errorf("exit %d, stdout %q; want exit 0 and %q", exit, stdout, "<h1>Hi</h1>\n")
	}
}
