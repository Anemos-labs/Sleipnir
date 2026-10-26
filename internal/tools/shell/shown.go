package shell

import (
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/shellparse"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/fs"
)

// recordShown counts as read the files a command's output showed whole (fs.RecordShown says when that is): the words of the
// command that can name a file, run from dir, are the candidates.
func recordShown(env *tools.Env, dir, command, output string) {
	var words []string
	for _, c := range shellparse.Parse(command).Commands {
		for _, a := range c.Args {
			if len(words) == maxShownWords {
				break
			}
			if a == "" || strings.HasPrefix(a, "-") || strings.ContainsAny(a, "$`") {
				continue
			}
			if !strings.ContainsAny(a, "*?[") {
				words = append(words, a)
				continue
			}
			// cat src/*.py: the shell expanded it in dir, and so does this (a cd before it only means fewer matches)
			pat := a
			if !filepath.IsAbs(pat) {
				pat = filepath.Join(dir, pat)
			}
			matches, _ := filepath.Glob(pat)
			words = append(words, matches[:min(len(matches), maxShownWords-len(words))]...)
		}
	}
	fs.RecordShown(env, dir, words, output)
}

// maxShownWords bounds the files one command can have looked at for recordShown.
const maxShownWords = 32
