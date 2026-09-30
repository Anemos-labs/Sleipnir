package mcp

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Protocol versions. A client is strict about what it sends (it offers exactly
// LatestProtocolVersion) and tolerant about what it receives: a server may
// answer with any well-formed revision date from OldestProtocolVersion on,
// because the revisions differ by additions (audio content, annotations,
// structured output, resource links) that this decoder accepts wherever they
// appear, and by transport details that the transports handle per negotiated
// version.
const (
	// LatestProtocolVersion is the newest revision implemented in full, and
	// the one offered in initialize.
	LatestProtocolVersion = "2025-06-18"
	// OldestProtocolVersion is the oldest accepted: the revision of the legacy
	// HTTP+SSE transport, which servers of that era still speak.
	OldestProtocolVersion = "2024-11-05"
)

// knownVersions are the revisions implemented in full (newest first).
var knownVersions = []string{LatestProtocolVersion, "2025-03-26", OldestProtocolVersion}

// checkVersion decides whether a version string from a server is acceptable.
// known reports a revision implemented in full; other acceptable values are
// later or in-between dates, tolerated as compatible.
func checkVersion(v string) (ok, known bool) {
	for _, k := range knownVersions {
		if v == k {
			return true, true
		}
	}
	if _, err := time.Parse("2006-01-02", v); err != nil || len(v) != len("2006-01-02") {
		return false, false
	}
	return v >= OldestProtocolVersion, false
}

// ServerInfo identifies the server (from initialize). Text is sanitised.
type ServerInfo struct {
	Name    string
	Title   string
	Version string
}

// ServerCapabilities are the capabilities a server declared in initialize.
type ServerCapabilities struct {
	Tools     *ToolsCapability
	Resources *ResourcesCapability
	Prompts   *PromptsCapability
	Logging   bool
	// Empty reports that the server declared no capabilities at all, which a
	// spec-following server with tools would never do.
	Empty bool
}

// ToolsCapability, ResourcesCapability and PromptsCapability carry the
// optional flags of each declared capability.
type (
	ToolsCapability     struct{ ListChanged bool }
	ResourcesCapability struct{ Subscribe, ListChanged bool }
	PromptsCapability   struct{ ListChanged bool }
)

// InitializeResult is the server's answer to initialize.
type InitializeResult struct {
	ProtocolVersion string
	Capabilities    ServerCapabilities
	ServerInfo      ServerInfo
	// Instructions are the server's usage hints, sanitised and capped. They
	// are untrusted text; the harness decides whether the model ever sees them.
	Instructions string
	// Tolerated is set when the server chose a protocol revision this client
	// does not implement in full but accepts as compatible (a later date).
	Tolerated bool
}

const maxInstructionsChars = 4000

func decodeInitialize(raw json.RawMessage) (*InitializeResult, error) {
	var w struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Title   string `json:"title"`
			Version string `json:"version"`
		} `json:"serverInfo"`
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("mcp: malformed initialize result: %w", jsonErrNoValue(err))
	}
	res := &InitializeResult{
		ProtocolVersion: w.ProtocolVersion,
		ServerInfo: ServerInfo{
			Name:    cleanMeta(w.ServerInfo.Name, 100),
			Title:   cleanMeta(w.ServerInfo.Title, 100),
			Version: cleanMeta(w.ServerInfo.Version, 50),
		},
		Instructions: cleanMeta(w.Instructions, maxInstructionsChars),
	}
	caps := &res.Capabilities
	caps.Empty = len(w.Capabilities) == 0
	present := func(k string) (json.RawMessage, bool) {
		v, ok := w.Capabilities[k]
		return v, ok && !isNull(v)
	}
	flags := func(v json.RawMessage) map[string]bool {
		var m map[string]bool
		_ = json.Unmarshal(v, &m) // tolerant: a capability with odd flags is still a capability
		return m
	}
	if v, ok := present("tools"); ok {
		caps.Tools = &ToolsCapability{ListChanged: flags(v)["listChanged"]}
	}
	if v, ok := present("resources"); ok {
		f := flags(v)
		caps.Resources = &ResourcesCapability{Subscribe: f["subscribe"], ListChanged: f["listChanged"]}
	}
	if v, ok := present("prompts"); ok {
		caps.Prompts = &PromptsCapability{ListChanged: flags(v)["listChanged"]}
	}
	_, caps.Logging = present("logging")
	return res, nil
}

// jsonErrNoValue rewrites encoding/json errors that quote input ("invalid
// character 'x' ...", type errors naming a Go field) into text that never
// echoes payload bytes: config and result bodies can hold credentials.
func jsonErrNoValue(err error) error {
	switch e := err.(type) {
	case *json.SyntaxError:
		return fmt.Errorf("invalid JSON at byte %d", e.Offset)
	case *json.UnmarshalTypeError:
		return fmt.Errorf("unexpected %s value for %s", e.Value, e.Field)
	}
	return err
}

// Limits applied while decoding lists from a server. They exist so a hostile
// or buggy server cannot turn a listing into unbounded memory.
const (
	maxNameBytes      = 128
	maxToolDescChars  = 4000
	maxTitleChars     = 200
	maxURIBytes       = 2048
	maxMimeBytes      = 128
	maxSchemaDecode   = 256 << 10
	maxPromptArgs     = 64
	maxPromptTextChar = 1 << 20
)

// Tool is one tool a server offers, after decoding and sanitising. The Name
// is exactly what the server sent (it must be echoed back in tools/call); if
// it is unusable, Problem says why and the tool is never exposed.
type Tool struct {
	Name        string
	Title       string
	Description string
	InputSchema json.RawMessage
	Annotations ToolAnnotations
	Problem     string
}

// ToolAnnotations are the behavioural hints of a tool. They come from the
// server and are untrusted: they inform prompts and scheduling, never
// permissions.
type ToolAnnotations struct {
	Title           string
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	OpenWorldHint   *bool
}

// ReadOnly reports an explicit readOnlyHint of true.
func (a ToolAnnotations) ReadOnly() bool { return a.ReadOnlyHint != nil && *a.ReadOnlyHint }

func decodeTool(raw json.RawMessage) Tool {
	var w struct {
		Name        string          `json:"name"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
		Annotations *struct {
			Title           string `json:"title"`
			ReadOnlyHint    *bool  `json:"readOnlyHint"`
			DestructiveHint *bool  `json:"destructiveHint"`
			IdempotentHint  *bool  `json:"idempotentHint"`
			OpenWorldHint   *bool  `json:"openWorldHint"`
		} `json:"annotations"`
	}
	t := Tool{}
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Problem = "malformed tool definition"
		return t
	}
	t.Name = w.Name
	switch {
	case w.Name == "":
		t.Problem = "tool has no name"
	case len(w.Name) > maxNameBytes:
		t.Problem = fmt.Sprintf("tool name is longer than %d bytes", maxNameBytes)
	case cleanStrict(w.Name) != w.Name || strings.ContainsAny(w.Name, "\n\t"):
		t.Problem = "tool name contains control or invisible characters"
	}
	t.Title = cleanMeta(w.Title, maxTitleChars)
	t.Description = cleanMeta(w.Description, maxToolDescChars)
	if len(w.InputSchema) > maxSchemaDecode {
		if t.Problem == "" {
			t.Problem = fmt.Sprintf("input schema is larger than %d bytes", maxSchemaDecode)
		}
	} else {
		t.InputSchema = w.InputSchema
	}
	if a := w.Annotations; a != nil {
		t.Annotations = ToolAnnotations{
			Title: cleanMeta(a.Title, maxTitleChars), ReadOnlyHint: a.ReadOnlyHint, DestructiveHint: a.DestructiveHint,
			IdempotentHint: a.IdempotentHint, OpenWorldHint: a.OpenWorldHint,
		}
	}
	return t
}

// Content is one item of a tool result or prompt message. Binary payloads are
// decoded from base64 and never rendered as text.
type Content struct {
	// Type is "text", "image", "audio", "resource_link", "resource", or an
	// unknown type from a newer server (kept so it can be reported).
	Type     string
	Text     string
	MIMEType string
	Data     []byte // image / audio payload
	URI      string
	Name     string
	Title    string
	// Description is set for resource links.
	Description string
	Size        int64
	Resource    *ResourceContents // embedded resource
	// BadData is set when a binary payload could not be decoded.
	BadData bool
}

// ResourceContents is the body of a resource: text or a blob.
type ResourceContents struct {
	URI      string
	MIMEType string
	Text     string
	Blob     []byte
	BadData  bool
}

type contentWire struct {
	Type        string        `json:"type"`
	Text        string        `json:"text"`
	Data        string        `json:"data"`
	MIMEType    string        `json:"mimeType"`
	URI         string        `json:"uri"`
	Name        string        `json:"name"`
	Title       string        `json:"title"`
	Description string        `json:"description"`
	Size        *int64        `json:"size"`
	Resource    *resourceWire `json:"resource"`
}

type resourceWire struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
}

// decodeBase64 is tolerant about padding and alphabet (servers in the wild use
// all four) and strict about size: the caller has already bounded the message.
func decodeBase64(s string) ([]byte, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

func decodeContent(raw json.RawMessage) Content {
	var w contentWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Content{Type: "invalid"}
	}
	c := Content{
		Type:        cleanMeta(w.Type, 40),
		MIMEType:    cleanMeta(w.MIMEType, maxMimeBytes),
		URI:         cleanMeta(w.URI, maxURIBytes),
		Name:        cleanMeta(w.Name, maxTitleChars),
		Title:       cleanMeta(w.Title, maxTitleChars),
		Description: cleanMeta(w.Description, 1000),
	}
	if w.Size != nil && *w.Size >= 0 {
		c.Size = *w.Size
	}
	switch c.Type {
	case "text":
		c.Text = cleanText(w.Text)
	case "image", "audio":
		if b, ok := decodeBase64(w.Data); ok {
			c.Data = b
		} else {
			c.BadData = true
		}
	case "resource":
		if w.Resource != nil {
			rc := &ResourceContents{
				URI:      cleanMeta(w.Resource.URI, maxURIBytes),
				MIMEType: cleanMeta(w.Resource.MIMEType, maxMimeBytes),
				Text:     cleanText(w.Resource.Text),
			}
			if w.Resource.Blob != "" {
				if b, ok := decodeBase64(w.Resource.Blob); ok {
					rc.Blob = b
				} else {
					rc.BadData = true
				}
			}
			c.Resource = rc
		}
	}
	return c
}

// CallToolResult is the outcome of tools/call.
type CallToolResult struct {
	Content []Content
	// StructuredContent is the JSON object a tool with an output schema
	// returns in addition to (per spec, also as text in) Content.
	StructuredContent json.RawMessage
	// IsError marks a tool execution error: the call reached the tool and the
	// tool failed. It is model-visible, unlike a protocol error.
	IsError bool
}

func decodeCallToolResult(raw json.RawMessage) (*CallToolResult, error) {
	var w struct {
		Content           []json.RawMessage `json:"content"`
		StructuredContent json.RawMessage   `json:"structuredContent"`
		IsError           bool              `json:"isError"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("mcp: malformed tools/call result: %w", jsonErrNoValue(err))
	}
	res := &CallToolResult{IsError: w.IsError}
	if len(w.StructuredContent) > 0 && !isNull(w.StructuredContent) {
		res.StructuredContent = w.StructuredContent
	}
	for _, c := range w.Content {
		res.Content = append(res.Content, decodeContent(c))
	}
	return res, nil
}

// Resource, ResourceTemplate: what resources/list and resources/templates/list
// return. Text is sanitised.
type Resource struct {
	URI         string
	Name        string
	Title       string
	Description string
	MIMEType    string
	Size        int64
}

// ResourceTemplate is an RFC 6570 URI template a server can resolve.
type ResourceTemplate struct {
	URITemplate string
	Name        string
	Title       string
	Description string
	MIMEType    string
}

func decodeResource(raw json.RawMessage) (Resource, bool) {
	var w struct {
		URI         string `json:"uri"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		MIMEType    string `json:"mimeType"`
		Size        *int64 `json:"size"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || w.URI == "" {
		return Resource{}, false
	}
	r := Resource{
		URI: cleanMeta(w.URI, maxURIBytes), Name: cleanMeta(w.Name, maxTitleChars), Title: cleanMeta(w.Title, maxTitleChars),
		Description: cleanMeta(w.Description, 1000), MIMEType: cleanMeta(w.MIMEType, maxMimeBytes),
	}
	if w.Size != nil && *w.Size >= 0 {
		r.Size = *w.Size
	}
	return r, true
}

func decodeResourceTemplate(raw json.RawMessage) (ResourceTemplate, bool) {
	var w struct {
		URITemplate string `json:"uriTemplate"`
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		MIMEType    string `json:"mimeType"`
	}
	if err := json.Unmarshal(raw, &w); err != nil || w.URITemplate == "" {
		return ResourceTemplate{}, false
	}
	return ResourceTemplate{
		URITemplate: cleanMeta(w.URITemplate, maxURIBytes), Name: cleanMeta(w.Name, maxTitleChars), Title: cleanMeta(w.Title, maxTitleChars),
		Description: cleanMeta(w.Description, 1000), MIMEType: cleanMeta(w.MIMEType, maxMimeBytes),
	}, true
}

// ReadResourceResult is the outcome of resources/read.
type ReadResourceResult struct{ Contents []ResourceContents }

func decodeReadResource(raw json.RawMessage) (*ReadResourceResult, error) {
	var w struct {
		Contents []resourceWire `json:"contents"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("mcp: malformed resources/read result: %w", jsonErrNoValue(err))
	}
	res := &ReadResourceResult{}
	for _, c := range w.Contents {
		rc := ResourceContents{
			URI: cleanMeta(c.URI, maxURIBytes), MIMEType: cleanMeta(c.MIMEType, maxMimeBytes), Text: cleanText(c.Text),
		}
		if c.Blob != "" {
			if b, ok := decodeBase64(c.Blob); ok {
				rc.Blob = b
			} else {
				rc.BadData = true
			}
		}
		res.Contents = append(res.Contents, rc)
	}
	return res, nil
}

// Prompt is a prompt template a server offers; the manager exposes each as a
// slash-command style entry.
type Prompt struct {
	Name        string
	Title       string
	Description string
	Arguments   []PromptArgument
	Problem     string
}

// PromptArgument is one argument of a prompt.
type PromptArgument struct {
	Name        string
	Title       string
	Description string
	Required    bool
}

func decodePrompt(raw json.RawMessage) Prompt {
	var w struct {
		Name        string `json:"name"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Arguments   []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Required    bool   `json:"required"`
		} `json:"arguments"`
	}
	p := Prompt{}
	if err := json.Unmarshal(raw, &w); err != nil {
		p.Problem = "malformed prompt definition"
		return p
	}
	p.Name = w.Name
	switch {
	case w.Name == "":
		p.Problem = "prompt has no name"
	case len(w.Name) > maxNameBytes:
		p.Problem = fmt.Sprintf("prompt name is longer than %d bytes", maxNameBytes)
	case cleanStrict(w.Name) != w.Name || strings.ContainsAny(w.Name, "\n\t"):
		p.Problem = "prompt name contains control or invisible characters"
	}
	p.Title = cleanMeta(w.Title, maxTitleChars)
	p.Description = cleanMeta(w.Description, 1000)
	for i, a := range w.Arguments {
		if i >= maxPromptArgs {
			break
		}
		if a.Name == "" {
			continue
		}
		p.Arguments = append(p.Arguments, PromptArgument{
			Name: cleanMeta(a.Name, 100), Title: cleanMeta(a.Title, maxTitleChars),
			Description: cleanMeta(a.Description, 500), Required: a.Required,
		})
	}
	return p
}

// PromptMessage is one message of an expanded prompt.
type PromptMessage struct {
	Role    string // "user" or "assistant"
	Content Content
}

// GetPromptResult is the expansion of a prompt.
type GetPromptResult struct {
	Description string
	Messages    []PromptMessage
}

// Text flattens the expansion into text for a slash command that hands it to
// the model as the user's message: each message's text in order (assistant
// messages are prefixed with "assistant:" so the exchange stays readable),
// blank-line separated. Non-text content becomes a one-line placeholder; nothing
// binary is forwarded.
func (r *GetPromptResult) Text() string {
	var parts []string
	for _, m := range r.Messages {
		var t string
		switch c := m.Content; c.Type {
		case "text":
			t = c.Text
		case "image", "audio":
			t = fmt.Sprintf("[%s: %s, %s; not shown]", c.Type, orDefault(c.MIMEType, "unknown type"), humanBytes(len(c.Data)))
		case "resource":
			if c.Resource != nil && c.Resource.Text != "" {
				t = fmt.Sprintf("[resource %s]\n%s", orUnknown(c.Resource.URI), c.Resource.Text)
			} else {
				t = "[resource omitted]"
			}
		case "resource_link":
			t = linkLine(c)
		default:
			t = fmt.Sprintf("[content of type %q omitted]", clipForError(c.Type))
		}
		if m.Role == "assistant" {
			t = "assistant: " + t
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n\n")
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func decodeGetPrompt(raw json.RawMessage) (*GetPromptResult, error) {
	var w struct {
		Description string `json:"description"`
		Messages    []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("mcp: malformed prompts/get result: %w", jsonErrNoValue(err))
	}
	res := &GetPromptResult{Description: cleanMeta(w.Description, 1000)}
	for _, m := range w.Messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role != "user" && role != "assistant" {
			role = "user"
		}
		content := decodeContent(m.Content)
		if content.Type == "text" {
			content.Text, _ = truncateRunes(content.Text, maxPromptTextChar)
		}
		res.Messages = append(res.Messages, PromptMessage{Role: role, Content: content})
	}
	return res, nil
}

// pageResult splits one page of a paginated listing into its items and the
// next cursor. A missing or null array is an empty page.
func pageResult(raw json.RawMessage, key string) (items []json.RawMessage, next string, err error) {
	var w map[string]json.RawMessage
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, "", fmt.Errorf("mcp: malformed %s result: %w", key, jsonErrNoValue(err))
	}
	if v, ok := w[key]; ok && !isNull(v) {
		if err := json.Unmarshal(v, &items); err != nil {
			return nil, "", fmt.Errorf("mcp: malformed %s list", key)
		}
	}
	if v, ok := w["nextCursor"]; ok && !isNull(v) {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			next = s
		}
	}
	return items, next, nil
}

// compactJSON returns v with insignificant whitespace removed. Requests are
// framed as one line each on stdio, so an argument object that arrives
// pretty-printed from a model must not break framing.
func compactJSON(v json.RawMessage) (json.RawMessage, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
