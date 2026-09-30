// Command mdlite reads Markdown on standard input and writes the HTML that mdlite.Render makes of it.
package main

import (
	"fmt"
	"io"
	"os"

	"example.com/mdlite"
)

func main() {
	src, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mdlite:", err)
		os.Exit(1)
	}
	if _, err := io.WriteString(os.Stdout, mdlite.Render(string(src))); err != nil {
		fmt.Fprintln(os.Stderr, "mdlite:", err)
		os.Exit(1)
	}
}
