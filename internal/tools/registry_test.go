package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// stub is a tool that has a spec and does nothing else.
type stub struct{ spec core.ToolSpec }

func (s stub) Spec() core.ToolSpec                         { return s.spec }
func (s stub) Run(context.Context, *Call) (*Result, error) { return &Result{Text: s.spec.Name}, nil }

func stubOf(name, schema string) stub {
	return stub{core.ToolSpec{Name: name, Description: "the " + name + " tool", InputSchema: json.RawMessage(schema)}}
}

// Every agent sends the same tool list, and the prompt is a byte-prefix cache key: what the registry hands out must not depend on the
// order the tools were registered in (a map is walked in a different order every time), nor on how a schema happened to be written.
func TestRegistrySpecsAreTheSameBytesWhateverTheOrderOfRegistration(t *testing.T) {
	all := []stub{
		stubOf("read", `{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer"}},"required":["path"]}`),
		stubOf("write", `{"required":["path","content"],"properties":{"content":{"type":"string"},"path":{"type":"string"}},"type":"object"}`),
		stubOf("bash", `{ "type" : "object", "properties" : { "command" : { "type" : "string" } } }`),
		stubOf("grep", `{"type":"object","properties":{"pattern":{"type":"string"}}}`),
		stubOf("edit", `{"type":"object"}`),
		stubOf("recall", `{"properties":{"handle":{"type":"string"}},"type":"object"}`),
	}
	var first []byte
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 50; i++ {
		r := NewRegistry()
		for _, j := range rng.Perm(len(all)) {
			r.Register(all[j])
		}
		specs, err := r.Specs()
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(specs)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = b
			continue
		}
		if !bytes.Equal(first, b) {
			t.Fatalf("registration order %d gave other bytes:\n%s\nwant\n%s", i, b, first)
		}
	}
	// sorted by name, and every schema canonical: keys in order, no white space
	var specs []core.ToolSpec
	if err := json.Unmarshal(first, &specs); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
		if strings.ContainsAny(string(s.InputSchema), " \n\t") && s.Name == "bash" {
			t.Errorf("the schema of %s is not canonical: %s", s.Name, s.InputSchema)
		}
	}
	if want := []string{"bash", "edit", "grep", "read", "recall", "write"}; !reflect.DeepEqual(names, want) {
		t.Errorf("specs in the order %v, want %v", names, want)
	}
	for _, s := range specs {
		if s.Name == "write" && string(s.InputSchema) != `{"properties":{"content":{"type":"string"},"path":{"type":"string"}},"required":["path","content"],"type":"object"}` {
			t.Errorf("the schema of write is %s", s.InputSchema)
		}
	}
}

func TestRegistryGetNamesAndReplacement(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("read"); ok || len(r.Names()) != 0 {
		t.Fatal("an empty registry has tools")
	}
	r.Register(stubOf("write", `{}`))
	r.Register(stubOf("read", `{}`))
	if got, want := r.Names(), []string{"read", "write"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names = %v, want %v, sorted", got, want)
	}
	// a tool registered under a name that is taken replaces it
	r.Register(stub{core.ToolSpec{Name: "read", Description: "the second read", InputSchema: json.RawMessage(`{}`)}})
	got, ok := r.Get("read")
	if !ok || got.Spec().Description != "the second read" {
		t.Errorf("Get(read) = %v, %v: the later tool should have replaced the first", got, ok)
	}
	if len(r.Names()) != 2 {
		t.Errorf("Names = %v after a replacement", r.Names())
	}
}

// A schema that is not JSON is the tool's defect and it is named, not the cause of a list that quietly lacks it.
func TestRegistrySpecsNameTheToolWhoseSchemaIsNotJSON(t *testing.T) {
	r := NewRegistry()
	r.Register(stubOf("fine", `{}`))
	r.Register(stubOf("broken", `{"type": `))
	if _, err := r.Specs(); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Specs error = %v, want one that names the tool", err)
	}
}

func TestErrorfIsAnErrorTheModelCanRead(t *testing.T) {
	res := Errorf("%s: no such file (%d)", "a.go", 2)
	if !res.IsError || res.Text != "a.go: no such file (2)" || res.Truncated {
		t.Errorf("Errorf = %+v", res)
	}
}

func TestNoGuardPermitsEverything(t *testing.T) {
	var g Guard = NoGuard{}
	if err := g.BeforeWrite("any", "/anywhere"); err != nil {
		t.Errorf("NoGuard refused a write: %v", err)
	}
	g.AfterWrite("any", "/anywhere") // nothing to observe, and it must not panic
}
