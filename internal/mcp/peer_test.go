package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// peer is the far end of an in-process pipe: a scripted server. Tests use it
// where the misbehaviour is the point (hangs, floods, malformed messages,
// oversized results), so the bytes are visible next to the assertion.
type peer struct {
	t     *testing.T
	lines chan string   // lines the client wrote
	eof   chan struct{} // closed when the client closed its end
	stop  chan struct{} // closed when the test ends, releasing helper goroutines

	wmu sync.Mutex
	out io.WriteCloser // toward the client
	in  io.ReadCloser  // from the client
}

// pair returns a client-side transport wired to a peer.
func pair(t *testing.T, opts StreamOptions) (*StreamTransport, *peer) {
	t.Helper()
	c2sR, c2sW := io.Pipe()
	s2cR, s2cW := io.Pipe()
	p := &peer{t: t, lines: make(chan string, 4096), eof: make(chan struct{}), stop: make(chan struct{}), out: s2cW, in: c2sR}
	go func() {
		sc := bufio.NewScanner(c2sR)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		for sc.Scan() {
			p.lines <- sc.Text()
		}
		close(p.eof)
	}()
	tr := NewStreamTransport(s2cR, c2sW, opts)
	t.Cleanup(func() {
		close(p.stop)
		_ = tr.Close()
		_ = s2cW.Close()
		_ = c2sR.Close()
		_ = s2cR.Close()
	})
	return tr, p
}

// next returns the next message the client sent, decoded loosely. It is safe to
// call from helper goroutines: when the test ends it returns nil quietly
// instead of failing a finished test.
func (p *peer) next() map[string]json.RawMessage {
	p.t.Helper()
	select {
	case l := <-p.lines:
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			p.t.Errorf("client sent invalid JSON %q: %v", l, err)
		}
		return m
	case <-p.stop:
	case <-time.After(5 * time.Second):
		select {
		case <-p.stop:
		default:
			p.t.Errorf("timed out waiting for the client to send something")
		}
	}
	return nil
}

// expect reads the next message and checks its method.
func (p *peer) expect(method string) map[string]json.RawMessage {
	p.t.Helper()
	m := p.next()
	if m == nil {
		return nil
	}
	var got string
	_ = json.Unmarshal(m["method"], &got)
	if got != method {
		p.t.Errorf("client sent %q, want %q (%v)", got, method, m)
	}
	return m
}

// nothing asserts the client sends nothing for a while.
func (p *peer) nothing(d time.Duration) {
	p.t.Helper()
	select {
	case l := <-p.lines:
		p.t.Fatalf("client sent %s, want nothing", l)
	case <-time.After(d):
	}
}

// send writes one raw line to the client.
func (p *peer) send(line string) {
	p.t.Helper()
	p.wmu.Lock()
	defer p.wmu.Unlock()
	if _, err := io.WriteString(p.out, line+"\n"); err != nil {
		p.t.Logf("peer send failed: %v", err)
	}
}

func (p *peer) sendf(format string, args ...any) { p.send(fmt.Sprintf(format, args...)) }

// reply answers a request with a result.
func (p *peer) reply(req map[string]json.RawMessage, result string) {
	p.t.Helper()
	p.sendf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, req["id"], result)
}

func (p *peer) replyErr(req map[string]json.RawMessage, code int, msg string) {
	p.t.Helper()
	m, _ := json.Marshal(msg)
	p.sendf(`{"jsonrpc":"2.0","id":%s,"error":{"code":%d,"message":%s}}`, req["id"], code, m)
}

// close hangs up on the client.
func (p *peer) close() { p.wmu.Lock(); _ = p.out.Close(); p.wmu.Unlock() }

const toolsCaps = `{"tools":{"listChanged":true},"prompts":{},"resources":{}}`

// handshake performs the server side of initialize with the given protocol
// version and capabilities JSON.
func (p *peer) handshake(version, caps string) {
	p.t.Helper()
	req := p.expect("initialize")
	p.reply(req, fmt.Sprintf(`{"protocolVersion":%q,"capabilities":%s,"serverInfo":{"name":"scripted","version":"0"}}`, version, caps))
	p.expect("notifications/initialized")
}

// connectPeer builds a connected client against a scripted peer that speaks
// the given capabilities.
func connectPeer(t *testing.T, caps string, copts ClientOptions, sopts StreamOptions) (*Client, *peer) {
	t.Helper()
	tr, p := pair(t, sopts)
	go p.handshake(LatestProtocolVersion, caps)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Connect(ctx, tr, copts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, p
}

func idOf(m map[string]json.RawMessage) string { return strings.TrimSpace(string(m["id"])) }
