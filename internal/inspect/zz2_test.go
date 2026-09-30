package inspect

import (
	"fmt"
	"os"
	"testing"
)

func TestDump2(t *testing.T) {
	dir := os.Getenv("DUMP_DIR")
	if dir == "" {
		t.Skip()
	}
	s, _ := Load(dir)
	for _, r := range s.Requests(RequestQuery{Tail: true, Limit: 500, Kind: "main"}).Requests {
		if len(r.Changed) > 0 {
			fmt.Println(r.ID, r.Changed, r.Rebase, r.Undeclared, r.ThreadFrom, r.ThreadTo, r.First)
		}
	}
	for _, tk := range s.Swarm().Tasks {
		fmt.Println(tk.ID, tk.Title, tk.Status, tk.Owner)
	}
}
