package wire

// CLISpec is every `sleipnir` command with its flags, as the runner builds forms from it (the shape of the mock's cli-spec.json).
type CLISpec struct {
	GeneratedFrom string       `json:"generatedFrom"`
	Generator     string       `json:"generator"`
	Commands      []CLICommand `json:"commands"`
	ChatSlash     []SlashEntry `json:"chatSlash"`
	ExitCodes     []ExitCode   `json:"exitCodes"`
}

// CLICommand is one command path with its usage, summary, positionals and flags; Mode says how the web runs it.
type CLICommand struct {
	Path       []string        `json:"path"`
	Usage      string          `json:"usage"`
	Summary    string          `json:"summary"`
	Positional []CLIPositional `json:"positional"`
	Flags      []CLIFlag       `json:"flags"`
	Source     string          `json:"source,omitempty"`
	Mode       string          `json:"mode"`
	Why        string          `json:"why,omitempty"`
}

// CLIPositional is a positional argument.
type CLIPositional struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// CLIFlag is one flag: Arg is bool, string, int, uint, float or duration.
type CLIFlag struct {
	Name        string `json:"name"`
	Arg         string `json:"arg"`
	Default     any    `json:"default"`
	DefaultNote string `json:"defaultNote,omitempty"`
	Repeatable  bool   `json:"repeatable"`
	Desc        string `json:"desc"`
}

// SlashEntry is a chat slash command for the / menu and the palette.
type SlashEntry struct {
	Cmd         string `json:"cmd"`
	Args        string `json:"args"`
	Desc        string `json:"desc"`
	Group       string `json:"group"`
	PaletteArgs string `json:"paletteArgs,omitempty"`
	PaletteDesc string `json:"paletteDesc,omitempty"`
	Custom      bool   `json:"custom,omitempty"`
}

// ExitCode is one exit status of the program and what it means.
type ExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// RunRequest runs a command: Path, positional values by name, flag values by name (strings, numbers, booleans; repeatable flags
// as arrays), and the tab whose directory it runs in (a privileged command also needs an X-Confirm id, CONTRACT.md 20).
type RunRequest struct {
	Path  []string          `json:"path"`
	Pos   map[string]string `json:"pos,omitempty"`
	Flags map[string]any    `json:"flags,omitempty"`
	Tab   string            `json:"tab,omitempty"`
}

// RunStarted is the id of a started run and its command line as the person would type it.
type RunStarted struct {
	ID      string `json:"id"`
	Cmdline string `json:"cmdline"`
}

// RunLine is one line of output: K is out, err, dim, head, ok, warn or bad.
type RunLine struct {
	K string `json:"k"`
	T string `json:"t"`
}

// RunCard is the result card: a title and labelled values.
type RunCard struct {
	Title string      `json:"title"`
	Rows  [][2]string `json:"rows"`
}

// RunResult is how a run ended.
type RunResult struct {
	Exit     int      `json:"exit"`
	Ms       int64    `json:"ms"`
	Card     *RunCard `json:"card,omitempty"`
	Canceled bool     `json:"canceled,omitempty"`
}

// DoctorStep is one request of a doctor probe as the Doctor page draws it.
type DoctorStep struct {
	OK     bool   `json:"ok"`
	Name   string `json:"name"`
	Ms     int64  `json:"ms"`
	Grp    string `json:"grp"`
	In     int    `json:"in"`
	Cached int    `json:"cached"`
	Out    int    `json:"out"`
}

// DoctorVerdict is the end of a doctor probe: what the endpoint does, warnings, and the summary card.
type DoctorVerdict struct {
	KV   [][2]string `json:"kv"`
	Warn []string    `json:"warn,omitempty"`
	Card *RunCard    `json:"card,omitempty"`
}

// RunFrame is the data of a "run" frame.
type RunFrame struct {
	ID      string         `json:"id"`
	Lines   []RunLine      `json:"lines,omitempty"`
	Step    *DoctorStep    `json:"step,omitempty"`
	Verdict *DoctorVerdict `json:"verdict,omitempty"`
	Result  *RunResult     `json:"result,omitempty"`
}

// RunInfo is a run in the recent-runs list.
type RunInfo struct {
	ID      string         `json:"id"`
	Path    []string       `json:"path"`
	Flags   map[string]any `json:"flags,omitempty"`
	Cmdline string         `json:"cmd"`
	Exit    *int           `json:"exit,omitempty"`
	Ms      int64          `json:"ms"`
	Running bool           `json:"running"`
}
