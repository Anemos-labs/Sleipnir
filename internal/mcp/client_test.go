package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type callResult struct {
	res *CallToolResult
	err error
}

// callAsync runs CallTool in the background so the test can play the server.
func callAsync(c *Client, ctx context.Context, name, args string, o CallOptions) <-chan callResult {
	ch := make(chan callResult, 1)
	go func() {
		r, err := c.CallTool(ctx, name, json.RawMessage(args), o)
		ch <- callResult{r, err}
	}()
	return ch
}

func await(t *testing.T, ch <-chan callResult, d time.Duration) callResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(d):
		t.Fatal("call did not finish")
	}
	return callResult{}
}

func TestHandshakeIsStrict(t *testing.T) {
	tr, p := pair(t, StreamOptions{})
	got := make(chan map[string]json.RawMessage, 1)
	go func() {
		req := p.expect("initialize")
		got <- req
		p.reply(req, `{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"s","version":"1"}}`)
		p.expect("notifications/initialized")
	}()
	c, err := Connect(context.Background(), tr, ClientOptions{Name: "harness", Version: "9.9"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := <-got

	var env struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Params  struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
		} `json:"params"`
	}
	raw, _ := json.Marshal(req)
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.JSONRPC != "2.0" || env.ID < 1 {
		t.Errorf("envelope: %+v", env)
	}
	if env.Params.ProtocolVersion != LatestProtocolVersion {
		t.Errorf("offered %q, want exactly %q", env.Params.ProtocolVersion, LatestProtocolVersion)
	}
	if len(env.Params.Capabilities) != 0 {
		t.Errorf("declared capabilities %v: sampling and elicitation are refused, roots need configuring", env.Params.Capabilities)
	}
	if env.Params.ClientInfo["name"] != "harness" || env.Params.ClientInfo["version"] != "9.9" {
		t.Errorf("clientInfo = %v", env.Params.ClientInfo)
	}
	if r := c.Initialized(); r == nil || r.ServerInfo.Name != "s" {
		t.Errorf("Initialized() = %+v", r)
	}
}

func TestHandshakeDeclaresRootsOnlyWhenConfigured(t *testing.T) {
	tr, p := pair(t, StreamOptions{})
	got := make(chan map[string]json.RawMessage, 1)
	go func() {
		req := p.expect("initialize")
		got <- req
		p.reply(req, `{"protocolVersion":"2025-06-18","capabilities":{}}`)
		p.expect("notifications/initialized")
	}()
	c, err := Connect(context.Background(), tr, ClientOptions{Roots: []Root{{Path: "/work/space", Name: "ws"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !strings.Contains(string((<-got)["params"]), `"roots":{"listChanged":false}`) {
		t.Error("roots capability not declared")
	}
}

func TestVersionNegotiation(t *testing.T) {
	tests := []struct {
		version   string
		ok        bool
		tolerated bool
	}{
		{"2025-06-18", true, false},
		{"2025-03-26", true, false},
		{"2024-11-05", true, false},
		{"2025-11-25", true, true}, // later dates: tolerated
		{"2026-03-01", true, true},
		{"2099-12-31", true, true},
		{"2025-01-01", true, true}, // in between and unknown: tolerated
		{"2024-10-07", false, false},
		{"2023-01-01", false, false},
		{"", false, false},
		{"latest", false, false},
		{"2025-13-45", false, false},
		{"2025-6-18", false, false},
		{" 2025-06-18", false, false},
		{"2025-06-18T00:00:00Z", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			tr, p := pair(t, StreamOptions{})
			go func() {
				req := p.expect("initialize")
				p.reply(req, fmt.Sprintf(`{"protocolVersion":%q,"capabilities":{"tools":{}}}`, tt.version))
				if tt.ok {
					p.expect("notifications/initialized")
				}
			}()
			c, err := Connect(context.Background(), tr, ClientOptions{})
			if !tt.ok {
				if !errors.Is(err, ErrProtocolVersion) {
					t.Fatalf("err = %v, want ErrProtocolVersion", err)
				}
				p.nothing(50 * time.Millisecond) // no initialized notification to a server we refuse
				return
			}
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer c.Close()
			if got := c.Initialized(); got.ProtocolVersion != tt.version || got.Tolerated != tt.tolerated {
				t.Errorf("version=%q tolerated=%v", got.ProtocolVersion, got.Tolerated)
			}
		})
	}
}

func TestCapabilityChecks(t *testing.T) {
	t.Run("declared capabilities gate the listing", func(t *testing.T) {
		c, p := connectPeer(t, `{"resources":{}}`, ClientOptions{}, StreamOptions{})
		if _, _, err := c.ListTools(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ListTools = %v, want ErrUnsupported", err)
		}
		if _, err := c.CallTool(context.Background(), "x", nil, CallOptions{}); !errors.Is(err, ErrUnsupported) {
			t.Errorf("CallTool = %v, want ErrUnsupported", err)
		}
		if _, _, err := c.ListPrompts(context.Background()); !errors.Is(err, ErrUnsupported) {
			t.Errorf("ListPrompts = %v", err)
		}
		p.nothing(50 * time.Millisecond) // nothing was asked of the server
	})
	t.Run("a server that declares nothing is probed", func(t *testing.T) {
		c, p := connectPeer(t, `{}`, ClientOptions{}, StreamOptions{})
		go func() { p.reply(p.expect("tools/list"), `{"tools":[{"name":"t","inputSchema":{"type":"object"}}]}`) }()
		tools, _, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 1 {
			t.Fatalf("%v %v", tools, err)
		}
	})
	t.Run("resources capability serves templates and reads", func(t *testing.T) {
		c, p := connectPeer(t, `{"resources":{}}`, ClientOptions{}, StreamOptions{})
		go func() {
			p.reply(p.expect("resources/templates/list"), `{"resourceTemplates":[{"uriTemplate":"file:///{p}","name":"f"}]}`)
		}()
		tpl, _, err := c.ListResourceTemplates(context.Background())
		if err != nil || len(tpl) != 1 || tpl[0].URITemplate != "file:///{p}" {
			t.Fatalf("%+v %v", tpl, err)
		}
	})
}

func TestEachCallerGetsItsOwnAnswer(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	const n = 30
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := c.CallTool(context.Background(), "echo", json.RawMessage(fmt.Sprintf(`{"i":%d}`, i)), CallOptions{})
			if err != nil {
				t.Errorf("call %d: %v", i, err)
				return
			}
			if got := r.Content[0].Text; got != fmt.Sprintf("answer-%d", i) {
				t.Errorf("call %d got %q", i, got)
			}
		}(i)
	}
	// Server: read all requests, answer them shuffled by arrival parity.
	var reqs []map[string]json.RawMessage
	for i := 0; i < n; i++ {
		reqs = append(reqs, p.expect("tools/call"))
	}
	order := make([]int, 0, n)
	for i := 0; i < n; i += 2 {
		order = append(order, i)
	}
	for i := 1; i < n; i += 2 {
		order = append(order, i)
	}
	for _, i := range order {
		var params struct {
			Arguments struct {
				I int `json:"i"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(reqs[i]["params"], &params)
		p.reply(reqs[i], fmt.Sprintf(`{"content":[{"type":"text","text":"answer-%d"}]}`, params.Arguments.I))
	}
	wg.Wait()
}

func TestIDToleranceAndUnexpectedResponses(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch := callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	req := p.expect("tools/call")
	// Noise first: responses to nothing we sent, a null id, a string id that is
	// not ours, a duplicate.
	p.send(`{"jsonrpc":"2.0","id":9999,"result":{}}`)
	p.send(`{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`)
	p.send(`{"jsonrpc":"2.0","id":"not-ours","result":{}}`)
	// Then the real answer with the id echoed as a numeric string.
	p.sendf(`{"jsonrpc":"2.0","id":"%s","result":{"content":[{"type":"text","text":"ok"}]}}`, idOf(req))
	r := await(t, ch, 5*time.Second)
	if r.err != nil || r.res.Content[0].Text != "ok" {
		t.Fatalf("%+v", r)
	}
	p.sendf(`{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"duplicate"}]}}`, idOf(req))
	time.Sleep(50 * time.Millisecond)
	if st := c.Stats(); st.Unexpected < 4 {
		t.Errorf("Unexpected = %d, want the 4 stray responses counted", st.Unexpected)
	}
	if c.Err() != nil {
		t.Errorf("stray responses must not disturb the connection: %v", c.Err())
	}
}

func TestServerErrorsBecomeRPCErrors(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch := callAsync(c, context.Background(), "x", `{}`, CallOptions{})
	p.replyErr(p.expect("tools/call"), -32602, "bad\x1b[31m params "+placeholder)
	r := await(t, ch, 5*time.Second)
	var rpc *RPCError
	if !errors.As(r.err, &rpc) || rpc.Code != -32602 {
		t.Fatalf("err = %v", r.err)
	}
	if strings.Contains(rpc.Message, "\x1b") {
		t.Errorf("server text not sanitised: %q", rpc.Message)
	}
}

func TestUnexpectedNotificationsAreIgnored(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	for _, l := range []string{
		`{"jsonrpc":"2.0","method":"notifications/totally/unknown","params":{"x":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":424242,"progress":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/progress","params":"not an object"}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`,
		`{"jsonrpc":"2.0","method":"notifications/resources/updated","params":{"uri":"x"}}`,
		`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"hi"}}`,
		`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`,
		`{"unknown":"shape","without":"method or id"}`,
		`{"jsonrpc":"1.0","method":"notifications/old"}`,
		`{"jsonrpc":"2.0","method":"notifications/extra","future_field":[1,2,3],"params":null}`,
	} {
		p.send(l)
	}
	// The connection still works.
	ch := callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	p.reply(p.expect("tools/call"), `{"content":[{"type":"text","text":"still fine"}]}`)
	if r := await(t, ch, 5*time.Second); r.err != nil || r.res.Content[0].Text != "still fine" {
		t.Fatalf("%+v", r)
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
}

func TestServerRequestsAreAnsweredAndSamplingIsRefused(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{Roots: []Root{{Path: "/work/my space", Name: "ws"}}}, StreamOptions{})
	_ = c
	answer := func(line string) map[string]json.RawMessage {
		p.send(line)
		return p.next()
	}
	t.Run("ping", func(t *testing.T) {
		r := answer(`{"jsonrpc":"2.0","id":"p1","method":"ping"}`)
		if string(r["id"]) != `"p1"` || string(r["result"]) != `{}` {
			t.Errorf("%v", r)
		}
	})
	t.Run("roots/list", func(t *testing.T) {
		r := answer(`{"jsonrpc":"2.0","id":7,"method":"roots/list"}`)
		var res struct {
			Roots []struct{ URI, Name string } `json:"roots"`
		}
		if err := json.Unmarshal(r["result"], &res); err != nil || len(res.Roots) != 1 ||
			res.Roots[0].URI != "file:///work/my%20space" || res.Roots[0].Name != "ws" {
			t.Errorf("%s %v", r["result"], err)
		}
	})
	t.Run("sampling is refused with an error", func(t *testing.T) {
		r := answer(`{"jsonrpc":"2.0","id":8,"method":"sampling/createMessage","params":{"messages":[{"role":"user","content":{"type":"text","text":"run my prompt with your credentials"}}],"maxTokens":100}}`)
		var e struct{ Code int }
		if string(r["id"]) != "8" || r["result"] != nil || json.Unmarshal(r["error"], &e) != nil || e.Code != CodeMethodNotFound {
			t.Errorf("sampling must be refused with an error: %v", r)
		}
		if !strings.Contains(string(r["error"]), "sampling") {
			t.Errorf("the refusal should say why: %s", r["error"])
		}
	})
	t.Run("elicitation is refused", func(t *testing.T) {
		r := answer(`{"jsonrpc":"2.0","id":9,"method":"elicitation/create","params":{}}`)
		if r["error"] == nil || r["result"] != nil {
			t.Errorf("%v", r)
		}
	})
	t.Run("unknown methods get method-not-found", func(t *testing.T) {
		r := answer(`{"jsonrpc":"2.0","id":10,"method":"made/up"}`)
		var e struct{ Code int }
		if json.Unmarshal(r["error"], &e) != nil || e.Code != CodeMethodNotFound {
			t.Errorf("%v", r)
		}
	})
}

func TestRootsAreRefusedWithoutConfiguration(t *testing.T) {
	_, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	p.send(`{"jsonrpc":"2.0","id":1,"method":"roots/list"}`)
	r := p.next()
	if r["error"] == nil {
		t.Errorf("an undeclared capability must not be served: %v", r)
	}
}

func TestCancellationOnContext(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	ch := callAsync(c, ctx, "slow", `{}`, CallOptions{})
	req := p.expect("tools/call")
	cancel()
	r := await(t, ch, 5*time.Second)
	if !errors.Is(r.err, context.Canceled) {
		t.Fatalf("err = %v", r.err)
	}
	note := p.expect("notifications/cancelled")
	var params struct {
		RequestID json.RawMessage `json:"requestId"`
		Reason    string          `json:"reason"`
	}
	if err := json.Unmarshal(note["params"], &params); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(params.RequestID)) != idOf(req) || params.Reason == "" {
		t.Errorf("cancelled params = %s, want requestId %s and a reason", note["params"], idOf(req))
	}
	if _, has := note["id"]; has {
		t.Error("a notification must not carry an id")
	}
	// A late response is ignored, and the connection is fine.
	p.reply(req, `{"content":[]}`)
	time.Sleep(50 * time.Millisecond)
	if c.Err() != nil || c.Stats().Unexpected != 1 {
		t.Errorf("err=%v unexpected=%d", c.Err(), c.Stats().Unexpected)
	}
}

func TestTimeoutSendsCancellation(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch := callAsync(c, context.Background(), "hang", `{}`, CallOptions{Timeout: 100 * time.Millisecond})
	req := p.expect("tools/call")
	r := await(t, ch, 5*time.Second)
	if !errors.Is(r.err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", r.err)
	}
	var to interface{ Timeout() bool }
	if !errors.As(r.err, &to) || !to.Timeout() {
		t.Errorf("error should satisfy the Timeout() contract: %v", r.err)
	}
	note := p.expect("notifications/cancelled")
	if !strings.Contains(string(note["params"]), `"requestId":`+idOf(req)) || !strings.Contains(string(note["params"]), "timed out") {
		t.Errorf("cancel notification = %s", note["params"])
	}
}

func TestInitializeIsNeverCancelled(t *testing.T) {
	tr, p := pair(t, StreamOptions{})
	go p.expect("initialize") // never answered
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := Connect(ctx, tr, ClientOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	p.nothing(100 * time.Millisecond) // no notifications/cancelled for initialize (spec)
}

func TestProgressExtendsTheInactivityTimeout(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	var seen atomic.Int32
	var lastMsg atomic.Value
	ch := callAsync(c, context.Background(), "long", `{}`, CallOptions{
		Timeout: 300 * time.Millisecond,
		OnProgress: func(pr Progress) {
			lastMsg.Store(pr.Message) // before the count: the test reads the message once the count says the last one ran
			seen.Add(1)
		},
	})
	req := p.expect("tools/call")
	var params struct {
		Meta struct {
			Token json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(req["params"], &params); err != nil || len(params.Meta.Token) == 0 {
		t.Fatalf("tools/call must carry a progress token: %s", req["params"])
	}
	// 8 x 100ms = 800ms, well past the 300ms inactivity timeout, but never silent.
	for i := 1; i <= 8; i++ {
		time.Sleep(100 * time.Millisecond)
		p.sendf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":%d,"total":8,"message":"step %d\u001b[31m"}}`, params.Meta.Token, i, i)
	}
	p.reply(req, `{"content":[{"type":"text","text":"finally"}]}`)
	r := await(t, ch, 5*time.Second)
	if r.err != nil || r.res.Content[0].Text != "finally" {
		t.Fatalf("a call that reports progress must survive its inactivity timeout: %+v", r)
	}
	deadline := time.Now().Add(2 * time.Second)
	for seen.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if seen.Load() != 8 {
		t.Errorf("progress callbacks = %d, want 8", seen.Load())
	}
	if m, _ := lastMsg.Load().(string); strings.Contains(m, "\x1b") || m != "step 8" {
		t.Errorf("progress message = %q (must be sanitised)", m)
	}
}

func TestProgressCannotHoldACallForever(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch := callAsync(c, context.Background(), "endless", `{}`, CallOptions{Timeout: time.Second, MaxTotal: 300 * time.Millisecond})
	req := p.expect("tools/call")
	var params struct {
		Meta struct {
			Token json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(req["params"], &params)
	stop := make(chan struct{})
	go func() {
		for i := 1; ; i++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				p.sendf(`{"jsonrpc":"2.0","method":"notifications/progress","params":{"progressToken":%s,"progress":%d}}`, params.Meta.Token, i)
			}
		}
	}()
	defer close(stop)
	r := await(t, ch, 5*time.Second)
	var te *timeoutError
	if !errors.As(r.err, &te) || !te.total {
		t.Fatalf("err = %v, want the absolute cap to fire", r.err)
	}
}

func TestListChangedIsCoalescedAndNeverLost(t *testing.T) {
	var mu sync.Mutex
	var kinds []ListKind
	release := make(chan struct{})
	first := make(chan struct{}, 1)
	_, p := connectPeer(t, toolsCaps, ClientOptions{OnListChanged: func(k ListKind) {
		mu.Lock()
		kinds = append(kinds, k)
		n := len(kinds)
		mu.Unlock()
		if n == 1 {
			first <- struct{}{}
			<-release // a slow consumer: the read path must not wait for it
		}
	}}, StreamOptions{})
	p.send(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	<-first
	// While the consumer is busy, hundreds more arrive: none may block the reader.
	for i := 0; i < 500; i++ {
		p.send(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/prompts/list_changed"}`)
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		gotPrompts := false
		for _, k := range kinds {
			gotPrompts = gotPrompts || k == ListPrompts
		}
		n := len(kinds)
		mu.Unlock()
		if gotPrompts && n >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("callbacks = %v: the last announcements must not be lost", kinds)
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) > 10 {
		t.Errorf("%d callbacks for 501 announcements: they should be coalesced", len(kinds))
	}
}

func TestCallbackPanicIsContained(t *testing.T) {
	var n atomic.Int32
	var logs []string
	var lmu sync.Mutex
	c, p := connectPeer(t, toolsCaps, ClientOptions{
		OnListChanged: func(ListKind) {
			if n.Add(1) == 1 {
				panic("consumer bug")
			}
		},
		Logf: func(f string, a ...any) { lmu.Lock(); logs = append(logs, fmt.Sprintf(f, a...)); lmu.Unlock() },
	}, StreamOptions{})
	p.send(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	time.Sleep(50 * time.Millisecond)
	p.send(`{"jsonrpc":"2.0","method":"notifications/tools/list_changed"}`)
	deadline := time.Now().Add(3 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n.Load() < 2 {
		t.Fatal("a panicking callback must not stop the dispatcher")
	}
	if c.Err() != nil {
		t.Fatal(c.Err())
	}
	lmu.Lock()
	defer lmu.Unlock()
	if len(logs) == 0 || !strings.Contains(logs[0], "panicked") {
		t.Errorf("panic not logged: %v", logs)
	}
}

func TestLogMessagesAreSanitisedAndBounded(t *testing.T) {
	got := make(chan LogMessage, 1000)
	_, p := connectPeer(t, toolsCaps, ClientOptions{OnLog: func(m LogMessage) {
		select {
		case got <- m:
		default:
		}
	}}, StreamOptions{})
	p.send(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"error","logger":"db","data":"boom \u001b]0;x\u0007 done"}}`)
	select {
	case m := <-got:
		if m.Level != "error" || m.Logger != "db" || m.Text != "boom  done" {
			t.Errorf("%+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no log message")
	}
	p.send(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":{"structured":true}}}`)
	select {
	case m := <-got:
		if !strings.Contains(m.Text, "structured") {
			t.Errorf("%+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no structured log message")
	}
}

func TestMalformedMessagesAreToleratedUntilTheyNeverStop(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	for _, l := range []string{`{"jsonrpc":`, `{not json}`, `{"a":1}x`, `[]`, `{"jsonrpc":"2.0"}`, `[1,2,3]`} {
		p.send(l)
	}
	ch := callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	p.reply(p.expect("tools/call"), `{"content":[{"type":"text","text":"ok"}]}`)
	if r := await(t, ch, 5*time.Second); r.err != nil {
		t.Fatalf("a few malformed messages must not break the connection: %v", r.err)
	}
	if st := c.Stats(); st.Malformed < 5 {
		t.Errorf("Malformed = %d", st.Malformed)
	}

	// A server that never sends anything valid is cut off.
	go func() {
		for i := 0; i < maxMalformedRun+100; i++ {
			if p.send(`{"jsonrpc":`) != nil {
				return // cut off, as intended
			}
		}
	}()
	select {
	case <-c.Done():
		if !errors.Is(c.Err(), ErrClosed) || !strings.Contains(c.Err().Error(), "malformed") {
			t.Errorf("err = %v", c.Err())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("endless malformed messages must end the connection")
	}
}

func TestBatchesFromServersAreAccepted(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	a := callAsync(c, context.Background(), "a", `{}`, CallOptions{})
	b := callAsync(c, context.Background(), "b", `{}`, CallOptions{})
	r1, r2 := p.expect("tools/call"), p.expect("tools/call")
	p.sendf(`[{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"one"}]}},{"jsonrpc":"2.0","method":"notifications/x"},{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"two"}]}}]`, r1["id"], r2["id"])
	got := map[string]bool{}
	for _, ch := range []<-chan callResult{a, b} {
		r := await(t, ch, 5*time.Second)
		if r.err != nil {
			t.Fatal(r.err)
		}
		got[r.res.Content[0].Text] = true
	}
	if !got["one"] || !got["two"] {
		t.Errorf("got %v", got)
	}
}

func TestCrashMidCallFailsCallsAndClosesTheClient(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch1 := callAsync(c, context.Background(), "a", `{}`, CallOptions{})
	ch2 := callAsync(c, context.Background(), "b", `{}`, CallOptions{})
	p.expect("tools/call")
	p.expect("tools/call")
	p.close() // the server dies
	for _, ch := range []<-chan callResult{ch1, ch2} {
		r := await(t, ch, 5*time.Second)
		if !errors.Is(r.err, ErrClosed) {
			t.Errorf("err = %v, want ErrClosed", r.err)
		}
	}
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed")
	}
	if _, err := c.CallTool(context.Background(), "x", nil, CallOptions{}); !errors.Is(err, ErrClosed) {
		t.Errorf("call after crash = %v", err)
	}
	if err := c.Ping(context.Background()); !errors.Is(err, ErrClosed) {
		t.Errorf("ping after crash = %v", err)
	}
	if !errors.Is(c.Err(), io.EOF) {
		t.Errorf("Err() should carry the cause: %v", c.Err())
	}
}

func TestHangingServerTimesOut(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{RequestTimeout: 100 * time.Millisecond}, StreamOptions{})
	go p.expect("tools/list")
	start := time.Now()
	_, _, err := c.ListTools(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("timeout took too long")
	}
	if c.Err() != nil {
		t.Errorf("a timed-out call must not kill the connection: %v", c.Err())
	}
}

func TestPagination(t *testing.T) {
	tool := func(n string) string { return fmt.Sprintf(`{"name":%q,"inputSchema":{"type":"object"}}`, n) }
	t.Run("follows cursors", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
		go func() {
			r1 := p.expect("tools/list")
			if strings.Contains(string(r1["params"]), "cursor") {
				t.Errorf("first page must have no cursor: %s", r1["params"])
			}
			p.reply(r1, `{"tools":[`+tool("a")+`,`+tool("b")+`],"nextCursor":"page2"}`)
			r2 := p.expect("tools/list")
			if !strings.Contains(string(r2["params"]), `"cursor":"page2"`) {
				t.Errorf("second page params = %s", r2["params"])
			}
			p.reply(r2, `{"tools":[`+tool("c")+`]}`)
		}()
		tools, warns, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 3 || len(warns) != 0 {
			t.Fatalf("%v %v %v", tools, warns, err)
		}
	})
	t.Run("a repeating cursor stops the listing", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
		go func() {
			for i := 0; i < 2; i++ { // the second page repeats the cursor: no third request
				p.reply(p.expect("tools/list"), `{"tools":[`+tool(fmt.Sprintf("t%d", i))+`],"nextCursor":"same"}`)
			}
		}()
		tools, warns, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 2 || len(warns) != 1 || !strings.Contains(warns[0], "repeated") {
			t.Fatalf("%d tools, warns %v, err %v", len(tools), warns, err)
		}
	})
	t.Run("page limit", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{MaxListPages: 3}, StreamOptions{})
		go func() {
			for i := 0; i < 3; i++ {
				p.reply(p.expect("tools/list"), fmt.Sprintf(`{"tools":[%s],"nextCursor":"c%d"}`, tool(fmt.Sprintf("t%d", i)), i))
			}
		}()
		tools, warns, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 3 || len(warns) != 1 || !strings.Contains(warns[0], "more than 3 pages") {
			t.Fatalf("%d tools, warns %v, err %v", len(tools), warns, err)
		}
	})
	t.Run("item limit", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{MaxListItems: 5}, StreamOptions{})
		var items []string
		for i := 0; i < 50; i++ {
			items = append(items, tool(fmt.Sprintf("t%02d", i)))
		}
		go func() { p.reply(p.expect("tools/list"), `{"tools":[`+strings.Join(items, ",")+`]}`) }()
		tools, warns, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 5 || len(warns) != 1 {
			t.Fatalf("%d tools, warns %v, err %v", len(tools), warns, err)
		}
	})
	t.Run("byte limit", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{MaxListBytes: 300}, StreamOptions{})
		go func() {
			p.reply(p.expect("tools/list"), `{"tools":[`+tool("a")+`],"nextCursor":"x"}`)
			p.reply(p.expect("tools/list"), `{"tools":[`+tool("b")+`,`+strings.Repeat(tool("c")+",", 20)+tool("d")+`]}`)
		}()
		tools, warns, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 1 || len(warns) != 1 || !strings.Contains(warns[0], "exceeds") {
			t.Fatalf("%d tools, warns %v, err %v", len(tools), warns, err)
		}
	})
	t.Run("a failing page fails the listing", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
		go func() {
			p.reply(p.expect("tools/list"), `{"tools":[`+tool("a")+`],"nextCursor":"x"}`)
			p.replyErr(p.expect("tools/list"), -32603, "internal")
		}()
		if _, _, err := c.ListTools(context.Background()); err == nil {
			t.Fatal("a partial tool list must not be returned as if complete")
		}
	})
	t.Run("junk items are tolerated", func(t *testing.T) {
		c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
		go func() {
			p.reply(p.expect("tools/list"), `{"tools":[`+tool("a")+`,42,"x",null,{"name":5},{"name":"b"}]}`)
		}()
		tools, _, err := c.ListTools(context.Background())
		if err != nil || len(tools) != 6 {
			t.Fatalf("%v %v", tools, err)
		}
		problems := 0
		for _, tl := range tools {
			if tl.Problem != "" {
				problems++
			}
		}
		if problems != 4 {
			t.Errorf("%d tools flagged with a problem, want the 4 unusable ones: %+v", problems, tools)
		}
	})
}

func TestOversizedResultFailsFastAndTheClientSurvives(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{MaxMessageBytes: 64 << 10})
	ch := callAsync(c, context.Background(), "big", `{}`, CallOptions{Timeout: 30 * time.Second})
	req := p.expect("tools/call")
	start := time.Now()
	// The TypeScript SDK puts result before id; the id must be found anyway.
	p.sendf(`{"result":{"content":[{"type":"text","text":%q}]},"jsonrpc":"2.0","id":%s}`, strings.Repeat("x", 1<<20), req["id"])
	r := await(t, ch, 5*time.Second)
	if !errors.Is(r.err, ErrMessageTooLarge) {
		t.Fatalf("err = %v, want ErrMessageTooLarge", r.err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("the call must fail as soon as the oversized answer is seen, not at its timeout")
	}
	ch = callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	p.reply(p.expect("tools/call"), `{"content":[{"type":"text","text":"still alive"}]}`)
	if r := await(t, ch, 5*time.Second); r.err != nil || r.res.Content[0].Text != "still alive" {
		t.Fatalf("client unusable after an oversized result: %+v", r)
	}
}

func TestChattyServerCannotWedgeTheClient(t *testing.T) {
	var progress atomic.Int64
	c, p := connectPeer(t, toolsCaps, ClientOptions{OnLog: func(LogMessage) { time.Sleep(time.Millisecond) }}, StreamOptions{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.send(`{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info","data":"spam"}}`)
			progress.Add(1)
		}
	}()
	defer func() { close(stop); wg.Wait() }()
	for progress.Load() < 2000 {
		time.Sleep(time.Millisecond)
	}
	// Mid-flood, a call still completes promptly: the slow OnLog consumer must
	// not stall the read path.
	ch := callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	go func() {
		req := p.expect("tools/call")
		p.reply(req, `{"content":[{"type":"text","text":"through the flood"}]}`)
	}()
	r := await(t, ch, 10*time.Second)
	if r.err != nil || r.res.Content[0].Text != "through the flood" {
		t.Fatalf("%+v", r)
	}
	if c.Stats().Dropped == 0 {
		t.Log("nothing was dropped; the machine kept up")
	}
}

func TestMaxInFlightAppliesBackpressure(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{MaxInFlight: 2}, StreamOptions{})
	var chans []<-chan callResult
	for i := 0; i < 4; i++ {
		chans = append(chans, callAsync(c, context.Background(), "echo", `{}`, CallOptions{}))
	}
	r1, r2 := p.expect("tools/call"), p.expect("tools/call")
	p.nothing(100 * time.Millisecond) // the third and fourth wait
	p.reply(r1, `{"content":[]}`)
	r3 := p.expect("tools/call")
	p.reply(r2, `{"content":[]}`)
	r4 := p.expect("tools/call")
	p.reply(r3, `{"content":[]}`)
	p.reply(r4, `{"content":[]}`)
	for _, ch := range chans {
		if r := await(t, ch, 5*time.Second); r.err != nil {
			t.Fatal(r.err)
		}
	}
}

func TestBackpressureWaitHonoursContext(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{MaxInFlight: 1}, StreamOptions{})
	first := callAsync(c, context.Background(), "echo", `{}`, CallOptions{})
	p.expect("tools/call")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := c.CallTool(ctx, "echo", nil, CallOptions{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	c.Close()
	<-first
}

func TestConcurrentCallsAreRaceFree(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	stop := make(chan struct{})
	go func() { // an echo server
		for {
			select {
			case <-stop:
				return
			case l := <-p.lines:
				var m map[string]json.RawMessage
				_ = json.Unmarshal([]byte(l), &m)
				if string(m["method"]) == `"tools/call"` {
					p.reply(m, `{"content":[{"type":"text","text":"ok"}]}`)
				}
			}
		}
	}()
	defer close(stop)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				r, err := c.CallTool(ctx, "echo", json.RawMessage(`{"g":1}`), CallOptions{})
				cancel()
				if err != nil || r.Content[0].Text != "ok" {
					t.Errorf("g%d i%d: %v %v", g, i, r, err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}

func TestCallToolRejectsNonObjectArguments(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	for _, args := range []string{`[1]`, `"str"`, `5`, `true`} {
		if _, err := c.CallTool(context.Background(), "x", json.RawMessage(args), CallOptions{}); err == nil {
			t.Errorf("arguments %s accepted", args)
		}
	}
	p.nothing(50 * time.Millisecond)
	// Empty arguments become {}.
	go func() {
		req := p.expect("tools/call")
		if !strings.Contains(string(req["params"]), `"arguments":{}`) {
			t.Errorf("params = %s", req["params"])
		}
		p.reply(req, `{"content":[]}`)
	}()
	if _, err := c.CallTool(context.Background(), "x", nil, CallOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestPrettyPrintedArgumentsStayOneLine(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	go func() { p.reply(p.expect("tools/call"), `{"content":[]}`) }()
	// A model may send arguments with newlines; on stdio that would split the frame.
	if _, err := c.CallTool(context.Background(), "x", json.RawMessage("{\n  \"a\": \"line1\\nline2\",\n  \"b\": [1,\n2]\n}"), CallOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestPing(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	go func() { p.reply(p.expect("ping"), `{}`) }()
	if err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseIsIdempotentAndFailsWaiters(t *testing.T) {
	c, p := connectPeer(t, toolsCaps, ClientOptions{}, StreamOptions{})
	ch := callAsync(c, context.Background(), "x", `{}`, CallOptions{})
	p.expect("tools/call")
	c.Close()
	c.Close()
	r := await(t, ch, 5*time.Second)
	if !errors.Is(r.err, ErrClosed) {
		t.Errorf("err = %v", r.err)
	}
}

func TestDecodeToolResultShapes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		chk  func(t *testing.T, r *CallToolResult)
	}{
		{"text", `{"content":[{"type":"text","text":"hi"}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].Text != "hi" || r.IsError {
				t.Errorf("%+v", r)
			}
		}},
		{"isError", `{"content":[{"type":"text","text":"bad"}],"isError":true}`, func(t *testing.T, r *CallToolResult) {
			if !r.IsError {
				t.Error("isError lost")
			}
		}},
		{"image std base64", `{"content":[{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}]}`, func(t *testing.T, r *CallToolResult) {
			if string(r.Content[0].Data) != "hello" || r.Content[0].MIMEType != "image/png" {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"image raw base64", `{"content":[{"type":"image","data":"aGVsbG8","mimeType":"image/png"}]}`, func(t *testing.T, r *CallToolResult) {
			if string(r.Content[0].Data) != "hello" {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"image url-safe base64", `{"content":[{"type":"image","data":"_-8","mimeType":"image/png"}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].BadData || len(r.Content[0].Data) != 2 {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"image bad base64", `{"content":[{"type":"image","data":"!!!","mimeType":"image/png"}]}`, func(t *testing.T, r *CallToolResult) {
			if !r.Content[0].BadData {
				t.Error("bad payload not flagged")
			}
		}},
		{"audio", `{"content":[{"type":"audio","data":"aGk=","mimeType":"audio/wav"}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].Type != "audio" || string(r.Content[0].Data) != "hi" {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"resource link", `{"content":[{"type":"resource_link","uri":"file:///a","name":"a","mimeType":"text/plain","description":"d","size":5}]}`, func(t *testing.T, r *CallToolResult) {
			c := r.Content[0]
			if c.URI != "file:///a" || c.Name != "a" || c.Size != 5 || c.Description != "d" {
				t.Errorf("%+v", c)
			}
		}},
		{"embedded text resource", `{"content":[{"type":"resource","resource":{"uri":"file:///a","mimeType":"text/plain","text":"body"}}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].Resource == nil || r.Content[0].Resource.Text != "body" {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"embedded blob resource", `{"content":[{"type":"resource","resource":{"uri":"x","blob":"AQID"}}]}`, func(t *testing.T, r *CallToolResult) {
			if got := r.Content[0].Resource.Blob; len(got) != 3 || got[2] != 3 {
				t.Errorf("%v", got)
			}
		}},
		{"structured", `{"content":[],"structuredContent":{"a":1}}`, func(t *testing.T, r *CallToolResult) {
			if string(r.StructuredContent) != `{"a":1}` {
				t.Errorf("%s", r.StructuredContent)
			}
		}},
		{"structured null", `{"content":[],"structuredContent":null}`, func(t *testing.T, r *CallToolResult) {
			if r.StructuredContent != nil {
				t.Errorf("%s", r.StructuredContent)
			}
		}},
		{"unknown type kept", `{"content":[{"type":"hologram","x":1}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].Type != "hologram" {
				t.Errorf("%+v", r.Content[0])
			}
		}},
		{"no content", `{}`, func(t *testing.T, r *CallToolResult) {
			if len(r.Content) != 0 {
				t.Errorf("%+v", r)
			}
		}},
		{"junk content item", `{"content":[7,"x",{"type":"text","text":"ok"}]}`, func(t *testing.T, r *CallToolResult) {
			if len(r.Content) != 3 || r.Content[2].Text != "ok" || r.Content[0].Type != "invalid" {
				t.Errorf("%+v", r.Content)
			}
		}},
		{"text is sanitised", `{"content":[{"type":"text","text":"a\u001b[31mb\u200bc"}]}`, func(t *testing.T, r *CallToolResult) {
			if r.Content[0].Text != "abc" {
				t.Errorf("%q", r.Content[0].Text)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := decodeCallToolResult(json.RawMessage(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			tt.chk(t, r)
		})
	}
	if _, err := decodeCallToolResult(json.RawMessage(`{"content":"not an array"}`)); err == nil {
		t.Error("a content field of the wrong type must be an error, not a silent empty result")
	} else if strings.Contains(err.Error(), "not an array") {
		t.Errorf("decode error echoes payload: %v", err)
	}
}

// finishedTransport fails every send the way a transport that has already ended
// does (with the raw cause), and tells the client why only when the test says so:
// the interval a child process's transport spends collecting an exit status.
type finishedTransport struct {
	mu sync.Mutex
	h  Handler
}

func (f *finishedTransport) Start(h Handler) error {
	f.mu.Lock()
	f.h = h
	f.mu.Unlock()
	return nil
}

func (f *finishedTransport) Send(context.Context, []byte) error {
	return &closedError{cause: io.EOF}
}

func (f *finishedTransport) Close() error { return nil }

func (f *finishedTransport) Ended() bool { return true }

func (f *finishedTransport) report(err error) {
	f.mu.Lock()
	h := f.h
	f.mu.Unlock()
	h.Closed(err)
}

func TestCallOnAFinishedTransportReportsTheRealCause(t *testing.T) {
	// The send fails with the raw cause (end of file) while the owner of the
	// transport is still finding out how the server died. The caller, and through
	// it the model, must be told the real reason, not "EOF".
	ft := &finishedTransport{}
	c := NewClient(ft, ClientOptions{})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !c.ended() || c.Err() != nil {
		t.Fatalf("ended=%v err=%v: the client should know the transport is over before it is told why", c.ended(), c.Err())
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		ft.report(errors.New("server process exited (exit status 3)"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := c.Ping(ctx)
	if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "exit status 3") || strings.Contains(err.Error(), "EOF") {
		t.Fatalf("err = %v", err)
	}
}

func TestCallOnAFinishedTransportDoesNotWaitLongerThanTheCaller(t *testing.T) {
	// A transport that never says why must not hold a call: the caller's own
	// deadline ends the wait and the raw cause is reported.
	ft := &finishedTransport{}
	c := NewClient(ft, ClientOptions{})
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Ping(ctx)
	if !errors.Is(err, ErrClosed) || !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("waited %v", d)
	}
}
