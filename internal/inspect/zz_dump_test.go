package inspect

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestDump(t *testing.T) {
	s, err := Load("../rl/traj/testdata/agent_run")
	if err != nil {
		t.Fatal(err)
	}
	sum := s.Summary()
	sum.Series.Points = sum.Series.Points[:3]
	b, _ := json.MarshalIndent(sum, "", " ")
	fmt.Println(string(b))
	c := s.Compactions()
	b, _ = json.MarshalIndent(c, "", " ")
	fmt.Println(string(b))
	rp, _ := s.Layers("be-1.10", true)
	b, _ = json.MarshalIndent(rp, "", " ")
	fmt.Println(string(b))
	rp2 := s.Requests(RequestQuery{Tail: true, Limit: 3})
	b, _ = json.MarshalIndent(rp2, "", " ")
	fmt.Println(string(b))
}
