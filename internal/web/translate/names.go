package translate

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// uiID is the agent id of the vocabulary: the single agent ("main") and the manager are "mgr" (VOCAB.md 8.1); every other id is
// as the swarm named it.
func uiID(id string) string {
	if id == "main" {
		return "mgr"
	}
	return id
}

// nonAgent reports whether a log's agent field names nobody of the vocabulary: the swarm runtime, the harness's own mail and board
// writes, the notes curator.
func nonAgent(id string) bool {
	switch id {
	case "", "swarm", "harness", "curator":
		return true
	}
	return false
}

// displayName is the tool's name as the page shows it (VOCAB.md 8.2).
func displayName(tool string) string {
	switch tool {
	case "bash", "bash_output", "bash_kill":
		return "Bash"
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "edit", "apply_patch":
		return "Edit"
	case "glob":
		return "Glob"
	case "grep":
		return "Grep"
	case "ls":
		return "Ls"
	case "web_fetch":
		return "WebFetch"
	case "web_search":
		return "WebSearch"
	case "plan":
		return "Plan"
	case "task":
		return "TaskBoard"
	case "spawn":
		return "Spawn"
	case "mail":
		return "Mail"
	case "note":
		return "Notes"
	case "wait":
		return "Wait"
	case "recall":
		return "Recall"
	case "skill":
		return "Skill"
	}
	if srv, name, ok := mcpName(tool); ok {
		return srv + "." + name
	}
	if tool == "" {
		return "Tool"
	}
	r := []rune(tool)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// mcpName splits an MCP tool name, mcp__server__tool.
func mcpName(tool string) (srv, name string, ok bool) {
	rest, found := strings.CutPrefix(tool, "mcp__")
	if !found {
		return "", "", false
	}
	srv, name, ok = strings.Cut(rest, "__")
	return srv, name, ok && srv != "" && name != ""
}

// callInput is the part of a tool call's arguments the page's rows read.
type callInput struct {
	Command  string          `json:"command"`
	Path     string          `json:"path"`
	FilePath string          `json:"file_path"`
	Content  *string         `json:"content"`
	Pattern  string          `json:"pattern"`
	URL      string          `json:"url"`
	Query    string          `json:"query"`
	Action   string          `json:"action"`
	ID       string          `json:"id"`
	IDs      []string        `json:"ids"`
	Title    string          `json:"title"`
	Text     string          `json:"text"`
	Task     string          `json:"task"`
	Role     string          `json:"role"`
	To       string          `json:"to"`
	Agent    string          `json:"agent"`
	Name     string          `json:"name"`
	Handle   string          `json:"handle"`
	Job      string          `json:"job"`
	Shell    string          `json:"shell_id"`
	Until    []string        `json:"until"`
	Items    json.RawMessage `json:"items"`
	Patch    string          `json:"patch"`
	Input    string          `json:"input"`
}

// maxDecodedInput bounds the arguments the translator decodes: a call with more is shown by its name.
const maxDecodedInput = 8 << 20

// decodeInput reads a tool call's arguments; a field of the wrong type is left out.
func decodeInput(raw json.RawMessage) callInput {
	var in callInput
	if len(raw) > 0 && len(raw) <= maxDecodedInput {
		_ = json.Unmarshal(raw, &in)
	}
	return in
}

// pathOf is the path a file tool names, project-relative with / separators when it is under root.
func pathOf(root string, in *callInput) string {
	return relPath(root, firstNonEmpty(in.Path, in.FilePath))
}

// relPath shows p relative to root when it lies under it, with / separators.
func relPath(root, p string) string {
	if p == "" {
		return ""
	}
	if root != "" && filepath.IsAbs(p) {
		if r, err := filepath.Rel(root, p); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			p = r
		}
	}
	return filepath.ToSlash(p)
}

// argOf is the one-line argument of a tool row (VOCAB.md 8.2): the command, the path, the pattern, what a swarm tool was asked.
// spawned names the agents a spawn call started, when the result said so.
func argOf(root, tool string, in *callInput, spawned string) string {
	switch tool {
	case "bash":
		return in.Command
	case "bash_output":
		return "output " + firstNonEmpty(in.ID, in.Job, in.Shell)
	case "bash_kill":
		return "kill " + firstNonEmpty(in.ID, in.Job, in.Shell)
	case "read", "write", "edit", "ls":
		return pathOf(root, in)
	case "apply_patch":
		return strings.Join(patchFiles(in.Patch, in.Input), ", ")
	case "glob":
		return firstNonEmpty(in.Pattern, pathOf(root, in))
	case "grep":
		return strings.TrimSpace(in.Pattern + " " + pathOf(root, in))
	case "web_fetch":
		if u, err := url.Parse(in.URL); err == nil && u.Host != "" {
			return u.Host + u.EscapedPath()
		}
		return in.URL
	case "web_search":
		return in.Query
	case "plan":
		var items []json.RawMessage
		if json.Unmarshal(in.Items, &items) == nil {
			return strconv.Itoa(len(items)) + " steps"
		}
		return ""
	case "task":
		v := in.Action
		ids := in.IDs
		if in.ID != "" {
			ids = append([]string{in.ID}, ids...)
		}
		switch {
		case len(ids) > 0:
			v += " " + strings.Join(ids, " ")
		case in.Title != "":
			v += " " + in.Title
		}
		return strings.TrimSpace(v)
	case "spawn":
		if spawned != "" {
			return spawned
		}
		return strings.TrimSpace(firstNonEmpty(in.Agent, in.Role) + " " + in.Task)
	case "mail":
		return "→ " + uiID(in.To)
	case "note":
		return firstNonEmpty(in.Action, "append")
	case "wait":
		if len(in.Until) > 0 {
			return strings.Join(in.Until, " ")
		}
		return "the team"
	case "recall":
		return firstNonEmpty(in.Handle, in.ID)
	case "skill":
		return in.Name
	}
	if _, _, ok := mcpName(tool); ok {
		return firstNonEmpty(in.Command, in.Path, in.Query, in.URL, in.Text, in.Name, in.ID)
	}
	return firstNonEmpty(in.Command, pathOf(root, in), in.Pattern, in.URL, in.Query, in.Action, in.Title, in.Text)
}

// patchHeader finds the files a patch names: unified diffs (+++ b/path) and the begin-patch format (*** Update File: path).
var patchHeader = regexp.MustCompile(`(?m)^(?:\+\+\+ (?:b/)?(\S+)|\*\*\* (?:Add|Update|Delete) File: (.+))$`)

// patchFiles lists the files a patch touches, in order, each once.
func patchFiles(patch, alt string) []string {
	p := firstNonEmpty(patch, alt)
	var out []string
	seen := map[string]bool{}
	for _, m := range patchHeader.FindAllStringSubmatch(p, 64) {
		f := strings.TrimSpace(firstNonEmpty(m[1], m[2]))
		if f == "" || f == "/dev/null" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// agentIDs finds agent ids (be-1, sc-12) in a text: the agents a spawn result names.
var agentIDs = regexp.MustCompile(`\b[a-z]{2,3}-[0-9]{1,3}\b`)

// spawnedIn names the agents a spawn call's result says it started, joined by spaces.
func spawnedIn(result string) string {
	seen := map[string]bool{}
	var out []string
	for _, id := range agentIDs.FindAllString(cutBytes(result, 4096), 32) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return strings.Join(out, " ")
}

// firstNonEmpty returns the first argument that is not empty.
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// roleCode is the short code of an agent id (be of be-1) and its number; the manager is mgr, 0.
func roleCode(id string) (code string, nth int) {
	if id == "mgr" || id == "main" {
		return "mgr", 0
	}
	i := strings.LastIndexByte(id, '-')
	if i <= 0 {
		return id, 0
	}
	n, err := strconv.Atoi(id[i+1:])
	if err != nil {
		return id, 0
	}
	return id[:i], n
}
