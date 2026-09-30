package mcp

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// envelope is any JSON-RPC 2.0 message, decoded loosely: which of the three
// kinds it is follows from which fields are present, not from a tag.
//
//	method + id       request (a server asking us something)
//	method, no id     notification
//	no method, id     response (result or error)
type envelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *wireError      `json:"error"`
}

type wireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// hasID reports a present, non-null id. Responses to requests the peer could
// not even parse carry "id": null; they answer nothing we sent.
func (e *envelope) hasID() bool {
	id := bytes.TrimSpace(e.ID)
	return len(id) > 0 && string(id) != "null"
}

// callID recovers the integer id of a request we sent from the id of a
// response. Ours are integers; a server that echoes it as a numeric string is
// tolerated (it is a common bug), anything else matches nothing.
func callID(raw json.RawMessage) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return 0, false
	}
	var n int64
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return 0, false
		}
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, false
		}
		n = v
	} else {
		var f float64
		if json.Unmarshal(raw, &f) != nil || float64(int64(f)) != f {
			return 0, false
		}
		n = int64(f)
	}
	// Our ids start at 1: anything else answers nothing we sent.
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// marshalRequest and friends build outgoing messages by hand rather than from
// the envelope struct: a response must carry "result" even when it is {}, and
// a request must not carry "result"/"error" at all. Being strict about what we
// send is half of the interoperability rule.
func marshalRequest(id int64, method string, params any) ([]byte, error) {
	p, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	b.WriteString(strconv.FormatInt(id, 10))
	b.WriteString(`,"method":`)
	m, _ := json.Marshal(method)
	b.Write(m)
	if p != nil {
		b.WriteString(`,"params":`)
		b.Write(p)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalNotification(method string, params any) ([]byte, error) {
	p, err := marshalParams(params)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","method":`)
	m, _ := json.Marshal(method)
	b.Write(m)
	if p != nil {
		b.WriteString(`,"params":`)
		b.Write(p)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshalResult answers a server request. id is echoed exactly as received.
func marshalResult(id json.RawMessage, result any) ([]byte, error) {
	r, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	b.Write(bytes.TrimSpace(id))
	b.WriteString(`,"result":`)
	b.Write(r)
	b.WriteByte('}')
	return b.Bytes(), nil
}

func marshalError(id json.RawMessage, code int, message string) []byte {
	msg, _ := json.Marshal(message)
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	if len(bytes.TrimSpace(id)) == 0 {
		b.WriteString("null")
	} else {
		b.Write(bytes.TrimSpace(id))
	}
	b.WriteString(`,"error":{"code":`)
	b.WriteString(strconv.Itoa(code))
	b.WriteString(`,"message":`)
	b.Write(msg)
	b.WriteString(`}}`)
	return b.Bytes()
}

func marshalParams(params any) ([]byte, error) {
	if params == nil {
		return nil, nil
	}
	if raw, ok := params.(json.RawMessage); ok {
		if len(raw) == 0 {
			return nil, nil
		}
		return compactJSON(raw)
	}
	return json.Marshal(params)
}

// skimmer follows a JSON-RPC message that is too large to keep, in constant
// memory, extracting only what is needed to fail the right call: the
// top-level "id", and whether a top-level "method" exists (a response has
// none).
//
// It exists because the alternative is bad both ways. Buffering an oversized
// message is the memory attack the size limit is for; dropping it silently
// leaves the caller waiting for its full timeout, and for a server that
// serialises "result" before "id" (the TypeScript SDK does) an id taken from
// a prefix would never be seen. So the whole message streams past this state
// machine, byte by byte, wherever the id happens to be.
type skimmer struct {
	st     skimState
	depth  int    // nesting depth; 1 = inside the top-level object
	esc    bool   // previous byte was a backslash inside a string
	nested bool   // inside a string within a nested value
	key    []byte // the key being read at depth 1 (capped)
	keyBad bool   // key longer than the cap: cannot be "id" or "method"
	which  int    // the depth-1 key just read: 0 other, 1 id, 2 method
	id     []byte // captured id text, quotes included for strings
	idBad  bool
	hasID  bool
	method bool
	ok     bool // saw a top-level object
}

type skimState uint8

const (
	skStart  skimState = iota // before the top-level '{'
	skKey                     // at depth 1, expecting a key or '}'
	skKeyStr                  // inside a key string
	skColon                   // after a key, expecting ':'
	skValue                   // expecting the first byte of a value
	skIDStr                   // inside the id string
	skIDNum                   // inside the id number
	skStr                     // inside some other string value at depth 1
	skNest                    // inside a nested object or array
	skScalar                  // inside a bare scalar at depth 1
	skAfter                   // after a value, expecting ',' or '}'
	skDone
)

const skimKeyCap = 16
const skimIDCap = 64

func isJSONSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// feed consumes bytes of the message.
func (s *skimmer) feed(p []byte) {
	for _, c := range p {
		s.step(c)
	}
}

func (s *skimmer) step(c byte) {
	switch s.st {
	case skDone:
	case skStart:
		if c == '{' {
			s.st, s.depth, s.ok = skKey, 1, true
		} else if !isJSONSpace(c) {
			s.st = skDone // not an object (a batch array, garbage): nothing to learn
		}
	case skKey:
		switch c {
		case '"':
			s.st, s.key, s.keyBad = skKeyStr, s.key[:0], false
		case '}':
			s.st = skDone
		}
	case skKeyStr:
		switch {
		case s.esc:
			s.esc = false
			s.keyByte(c) // an escaped key is not "id" or "method" as far as we care
		case c == '\\':
			s.esc = true
			s.keyBad = true
		case c == '"':
			s.st = skColon
			s.which = 0
			if !s.keyBad {
				switch string(s.key) {
				case "id":
					s.which = 1
				case "method":
					s.which = 2
				}
			}
		default:
			s.keyByte(c)
		}
	case skColon:
		if c == ':' {
			s.st = skValue
		}
	case skValue:
		if isJSONSpace(c) {
			return
		}
		if s.which == 2 {
			s.method = true
		}
		switch {
		case s.which == 1 && c == '"':
			s.st, s.id, s.idBad = skIDStr, append(s.id[:0], c), false
		case s.which == 1 && (c == '-' || c >= '0' && c <= '9'):
			s.st, s.id, s.idBad = skIDNum, append(s.id[:0], c), false
		case c == '"':
			s.st = skStr
		case c == '{' || c == '[':
			s.st, s.depth = skNest, 2
		default:
			s.st = skScalar
			if s.which == 1 {
				s.id, s.idBad = s.id[:0], true // null, true, object...: no usable id
			}
		}
	case skIDStr:
		switch {
		case s.esc:
			s.esc = false
			s.idByte(c)
		case c == '\\':
			s.esc = true
			s.idByte(c)
		case c == '"':
			s.idByte(c)
			s.hasID = !s.idBad
			s.st = skAfter
		default:
			s.idByte(c)
		}
	case skIDNum:
		switch {
		case c == ',':
			s.hasID, s.st = !s.idBad, skKey
		case c == '}':
			s.hasID, s.st = !s.idBad, skDone
		case isJSONSpace(c):
			s.hasID, s.st = !s.idBad, skAfter
		default:
			s.idByte(c)
		}
	case skStr:
		switch {
		case s.esc:
			s.esc = false
		case c == '\\':
			s.esc = true
		case c == '"':
			s.st = skAfter
		}
	case skNest:
		switch {
		case s.esc:
			s.esc = false
		case s.nested:
			switch c {
			case '\\':
				s.esc = true
			case '"':
				s.nested = false
			}
		case c == '"':
			s.nested = true
		case c == '{' || c == '[':
			s.depth++
		case c == '}' || c == ']':
			s.depth--
			if s.depth == 1 {
				s.st = skAfter
			}
		}
	case skScalar:
		switch c {
		case ',':
			s.st = skKey
		case '}':
			s.st = skDone
		}
	case skAfter:
		switch c {
		case ',':
			s.st = skKey
		case '}':
			s.st = skDone
		}
	}
}

func (s *skimmer) keyByte(c byte) {
	if len(s.key) >= skimKeyCap {
		s.keyBad = true
		return
	}
	s.key = append(s.key, c)
}

func (s *skimmer) idByte(c byte) {
	if len(s.id) >= skimIDCap {
		s.idBad = true
		return
	}
	s.id = append(s.id, c)
}

// result returns the id (valid JSON: a number or a quoted string), whether a
// top-level "method" was present, and whether anything could be learned.
func (s *skimmer) result() (id json.RawMessage, hasMethod, ok bool) {
	if s.st == skIDNum { // message ended while reading the number (truncated)
		s.hasID = !s.idBad
	}
	if !s.ok {
		return nil, false, false
	}
	if s.hasID && json.Valid(s.id) {
		return json.RawMessage(append([]byte(nil), s.id...)), s.method, true
	}
	return nil, s.method, true
}
