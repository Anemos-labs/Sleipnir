package main

import (
	"github.com/anemos-labs/sleipnir/internal/web/seam"
	"github.com/anemos-labs/sleipnir/internal/web/translate"
)

// init makes the translator of every tab generation internal/web/translate's (the stand-in of web_translate.go is then unused).
func init() {
	newTranslator = func(c translatorConfig) seam.Translator {
		return translate.New(translate.Config{Tab: c.Tab, Gen: c.Gen, Root: c.Root, StartedAt: c.StartedAt, Publish: c.Publish, Now: c.Now, Verify: c.Verify})
	}
}
