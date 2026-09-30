package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestDump(t *testing.T) {
	dir := os.Getenv("DUMP_DIR")
	if dir == "" {
		t.Skip()
	}
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := s.Summary()
	sum.Series.Points = nil
	sum.Cost.Assumptions = nil
	b, _ := json.MarshalIndent(sum, "", " ")
	fmt.Println(string(b))
	for _, name := range []string{"anom", "swarm", "agents"} {
		var v any
		switch name {
		case "anom":
			v = s.Anomalies()
		case "swarm":
			sw := s.Swarm()
			sw.Agents = nil
			v = sw
		case "agents":
			ag := s.Agents()
			for i := range ag {
				ag[i].SparkCtx, ag[i].SparkHit = nil, nil
			}
			v = ag
		}
		b, _ := json.MarshalIndent(v, "", " ")
		fmt.Println(name, string(b))
	}
}
