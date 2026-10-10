package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := WriteJSON(rec, 201, map[string]string{"text": "<img src=x onerror=alert(1)> & more"}); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 201 || rec.Header().Get("Content-Type") != "application/json; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("response = %d %v", rec.Code, rec.Header())
	}
	body := rec.Body.String()
	if strings.ContainsAny(body, "<>&") || !strings.Contains(body, uesc("003c")+"img") {
		t.Errorf("text from a repository or a model must stay inert even if a client misreads the type: %s", body)
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %s for %d bytes", rec.Header().Get("Content-Length"), len(body))
	}
	var back map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &back); err != nil || back["text"] != "<img src=x onerror=alert(1)> & more" {
		t.Errorf("round trip = %v %v", back, err)
	}
	// A value that cannot be encoded is a clean 500 and an error for the caller, not a truncated 200.
	rec = httptest.NewRecorder()
	err := WriteJSON(rec, 200, map[string]any{"c": make(chan int)})
	if err == nil || rec.Code != 500 || errCode(rec) != "internal" || strings.Contains(rec.Body.String(), "chan") {
		t.Errorf("unencodable = %v %d %s", err, rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	if err := WriteJSON(rec, 200, json.RawMessage(`{"a":"<b>"}`)); err != nil || strings.Contains(rec.Body.String(), "<") {
		t.Errorf("raw JSON is escaped as well: %s", rec.Body.String())
	}
}

func TestErrorBody(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, 418, "teapot", "short and stout")
	if rec.Code != 418 || rec.Header().Get("Cache-Control") != "no-store" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("response = %d %v", rec.Code, rec.Header())
	}
	var e errorBody
	mustJSON(t, rec, &e)
	if e.Code != "teapot" || e.Error != "short and stout" {
		t.Errorf("body = %+v", e)
	}
	rec = httptest.NewRecorder()
	Error(rec, 400, "x", strings.Repeat("é", 1000))
	mustJSON(t, rec, &e)
	if len(e.Error) > maxErrorMessage+3 || !strings.HasSuffix(e.Error, "...") {
		t.Errorf("a long message is cut: %d bytes", len(e.Error))
	}
	rec = httptest.NewRecorder()
	Error(rec, 400, "x", "<b>"+string(rune(0))+"</b>")
	if strings.ContainsAny(rec.Body.String(), "<>") {
		t.Errorf("message is not escaped: %s", rec.Body.String())
	}
}

func TestDecodeJSONOutsideTheEnvelopeStillBoundsTheBody(t *testing.T) {
	hr := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"`+strings.Repeat("x", int(DefaultMaxBody))+`"}`))
	rec := httptest.NewRecorder()
	var v struct{ A string }
	if DecodeJSON(rec, hr, &v) || rec.Code != 413 {
		t.Errorf("an oversized body outside the envelope = %d", rec.Code)
	}
	hr = httptest.NewRequest("POST", "/", strings.NewReader(`{"a":"x"}`))
	rec = httptest.NewRecorder()
	if !DecodeJSON(rec, hr, &v) || v.A != "x" {
		t.Errorf("a good body: %d %+v", rec.Code, v)
	}
	// A nil body (a request that was built by hand) is an empty body.
	hr = httptest.NewRequest("POST", "/", nil)
	rec = httptest.NewRecorder()
	if DecodeJSON(rec, hr, &v) || rec.Code != 400 {
		t.Errorf("no body = %d", rec.Code)
	}
}

func TestLogfOutsideTheEnvelopeDoesNothing(t *testing.T) {
	Logf(httptest.NewRequest("GET", "/", nil), "no server to log to %d", 1)
	if RequestID(httptest.NewRequest("GET", "/", nil)) != "" {
		t.Error("a request outside the envelope has no id")
	}
}

func TestRequestIDsAreSentAndDiffer(t *testing.T) {
	rg := newRig(t, nil)
	a := rg.get("/api/ping").Header().Get("X-Request-Id")
	b := rg.get("/api/ping").Header().Get("X-Request-Id")
	if len(a) != 12 || a == b {
		t.Errorf("request ids %q %q", a, b)
	}
	var out struct{ Rid string }
	rec := rg.get("/api/log")
	mustJSON(t, rec, &out)
	if out.Rid != rec.Header().Get("X-Request-Id") {
		t.Errorf("RequestID = %q, header %q", out.Rid, rec.Header().Get("X-Request-Id"))
	}
}

func TestCleanPath(t *testing.T) {
	for p, want := range map[string]bool{
		"/": true, "/a": true, "/a/b": true, "/a/": true, "/api/": true, "": false, "a": false, "//": false, "/a//b": false, "/./a": false,
		"/a/./b": false, "/a/../b": false, "/..": false, "/a\\b": false, "/a\x00": false, "/a/b/..": false, "/a/.": false,
	} {
		if got := cleanPath(p); got != want {
			t.Errorf("cleanPath(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestContentTypeRule(t *testing.T) {
	for v, want := range map[string]bool{
		"application/json": true, "application/json; charset=utf-8": true, "APPLICATION/JSON;CHARSET=UTF-8": true, "application/json;charset=\"utf-8\"": true,
		"": false, "text/plain": false, "application/json; charset=utf-16": false, "application/json; x=y": false, "application/jsonx": false,
		"application/json, text/plain": false, "multipart/form-data": false, "application/x-www-form-urlencoded": false, "json": false,
	} {
		if got := isJSONContentType(v); got != want {
			t.Errorf("isJSONContentType(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestEventAndErrorTypesDocumentedInThePackageAreTheOnesTheServerUses(t *testing.T) {
	// Every code the envelope emits is listed in doc.go: a client can rely on the list.
	rg := newRig(t, nil)
	cookie := rg.login()
	var seen = map[string]bool{}
	for _, r := range []req{
		{target: "/api/ping"}, {target: "/api/ping", host: "evil.example"},
		{method: "POST", target: "/api/echo/1", header: map[string]string{"Cookie": cookie, "Origin": "http://evil.example"}},
		{target: "/api/ping", header: map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site"}},
		{method: "POST", target: "/api/echo/1", header: map[string]string{"Cookie": cookie, "Origin": testOrigin}},
		{method: "POST", target: "/api/echo/1", header: map[string]string{"Cookie": cookie, "Origin": testOrigin, RequestHeader: "1", "Content-Type": "text/plain"}, body: "x"},
		{method: "POST", target: "/api/echo/1", header: browser(cookie), body: "{"},
		{target: "/api/nothing", header: map[string]string{"Cookie": cookie}},
		{method: "POST", target: "/api/ping", header: browser(cookie)},
		{method: "POST", target: "/api/danger", header: browser(cookie), body: "{}"},
		{method: "POST", target: "/api/danger", header: mergeHeaders(browser(cookie), map[string]string{ConfirmHeader: "nope"}), body: "{}"},
		{target: "/api/boom", header: map[string]string{"Cookie": cookie}},
	} {
		seen[errCode(rg.do(r))] = true
	}
	src, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.Join(strings.Fields(string(src)), " ")
	for code := range seen {
		if code == "" || !strings.Contains(doc, " "+code+",") && !strings.Contains(doc, " "+code+".") {
			t.Errorf("code %q is not in the list of doc.go", code)
		}
	}
	if len(seen) < 10 {
		t.Errorf("only %d codes were provoked: %v", len(seen), seen)
	}
}
