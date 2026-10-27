package perm

import "strings"

// Persistence is what a model plants to run after its session: a line in a shell's startup file, a cron job, a service, a login item,
// a global git hook, a Python .pth file, a package index of its choosing. A coding task almost never needs one, and a model that was
// trained to act on a trigger (a backdoored or "sleeper" model) needs exactly one to outlive the run. So a write there, and the
// commands that install one, are judged like the high-risk class: asked about when someone can answer, refused in bypass mode with the
// reason (so the agent finds another way or says what it needs), allowed in yolo mode, and allowed in every mode by an Allow rule that
// names the file or the exact command. Commands that print a stored credential (gh auth token) are judged the same way: the token
// would land in the transcript, which the provider and the session log keep, and the tool that holds it can use it without showing it.

// persistHome are files and directories under $HOME, relative to it and lower-case (protections compare folded paths), whose contents
// run later or decide what runs later.
var persistHome = []struct{ rel, what string }{
	// shell startup files: every new shell runs them
	{"/.bashrc", "a shell startup file"}, {"/.bash_profile", "a shell startup file"}, {"/.bash_login", "a shell startup file"},
	{"/.bash_logout", "a shell startup file"}, {"/.profile", "a shell startup file"}, {"/.zshrc", "a shell startup file"},
	{"/.zshenv", "a shell startup file"}, {"/.zprofile", "a shell startup file"}, {"/.zlogin", "a shell startup file"},
	{"/.zlogout", "a shell startup file"}, {"/.kshrc", "a shell startup file"}, {"/.cshrc", "a shell startup file"},
	{"/.tcshrc", "a shell startup file"}, {"/.config/fish", "a shell startup directory"},
	{"/.xprofile", "a session startup file"}, {"/.xinitrc", "a session startup file"}, {"/.xsessionrc", "a session startup file"},
	{"/.pam_environment", "the login environment"}, {"/.config/environment.d", "the login environment"},
	// services, timers and login items
	{"/.config/systemd/user", "a user service directory"}, {"/.config/autostart", "a login item directory"},
	{"/library/launchagents", "a launch agent directory"},
	// programs that shadow others on $PATH
	{"/.local/bin", "a directory on $PATH"}, {"/bin", "a directory on $PATH"},
	// git's global configuration: hooks, ssh command, credential helper, aliases and URL rewrites for every repository
	{"/.gitconfig", "git's global configuration"}, {"/.config/git/config", "git's global configuration"},
	// editors and interpreters that run their startup code
	{"/.vimrc", "an editor startup file"}, {"/.exrc", "an editor startup file"}, {"/.config/nvim/init.vim", "an editor startup file"},
	{"/.config/nvim/init.lua", "an editor startup file"}, {"/.emacs", "an editor startup file"},
	{"/.emacs.d/init.el", "an editor startup file"}, {"/.ipython/profile_default/startup", "an interpreter startup directory"},
	// where packages are installed from
	{"/.pip/pip.conf", "the package index configuration"}, {"/.config/pip/pip.conf", "the package index configuration"},
	{"/.pydistutils.cfg", "the package index configuration"}, {"/.cargo/config", "the package index configuration"},
	{"/.cargo/config.toml", "the package index configuration"}, {"/.m2/settings.xml", "the package index configuration"},
	{"/.yarnrc", "the package index configuration"}, {"/.yarnrc.yml", "the package index configuration"},
	{"/.gemrc", "the package index configuration"}, {"/.bundle/config", "the package index configuration"},
}

// persistSystem are directories outside any home with the same role (most need root, which a container often is).
var persistSystem = []struct{ dir, what string }{
	{"/var/spool/cron", "the cron spool"}, {"/var/spool/at", "the at spool"},
	{"/library/launchagents", "a launch agent directory"}, {"/library/launchdaemons", "a launch daemon directory"},
}

// persists says why a write is persistence (see above), or "" when it is not. Reads are never: they plant nothing. A recursive
// write to a directory that holds such a place (cp -r dotfiles/. ~) is persistence too, unless the operand only might be a file or
// is a formatter's.
func (rs *resolver) persists(a access) string {
	if !a.write || a.dynamic {
		return ""
	}
	for _, f := range uniq(fold(a.lex), fold(a.real)) {
		if why := rs.persistsForm(f, a); why != "" {
			return why
		}
	}
	return ""
}

func (rs *resolver) persistsForm(f string, a access) string {
	shown := rs.display(a.real)
	if shown == "" {
		shown = a.raw
	}
	wide := a.tree && !a.guess && !a.format
	what := func(p, kind string) string {
		return shown + " is " + kind + " (" + p + "): what is written there runs after this session"
	}
	for _, h := range uniq(fold(rs.home), fold(rs.homeReal)) {
		if h == "" || h == "/" {
			continue
		}
		if rs.worksIn(h, f) {
			continue
		}
		for _, p := range persistHome {
			if inside(h+p.rel, f) {
				return what("~"+p.rel, p.what)
			}
			if wide && f != h+p.rel && inside(f, h+p.rel) {
				return shown + " contains ~" + p.rel + " (" + p.what + "); name a narrower path"
			}
		}
	}
	for _, p := range persistSystem {
		if inside(p.dir, f) {
			return what(p.dir, p.what)
		}
	}
	// Python runs every .pth file of a site-packages directory, and sitecustomize/usercustomize, at each start. A project's own
	// virtual environment is the project's business.
	if !rs.inStrictWorkspace(f) {
		segs := splitSegs(f)
		if n := len(segs); n > 1 && (segs[n-2] == "site-packages" || segs[n-2] == "dist-packages") {
			if b := segs[n-1]; strings.HasSuffix(b, ".pth") || b == "sitecustomize.py" || b == "usercustomize.py" {
				return shown + " is Python start-up code outside the workspace: every Python program runs it"
			}
		}
	}
	return ""
}

// worksIn reports whether f is in a workspace root that does not hold the home directory h: a person who chose ~/.config/nvim as the
// project has said that editing it is the task. A root at or above h (the home directory as the project, or "/") says nothing about
// the files of h.
func (rs *resolver) worksIn(h, f string) bool {
	for _, r := range rs.roots {
		for _, root := range uniq(fold(r.lex), fold(r.real)) {
			if inside(root, f) && !inside(root, h) {
				return true
			}
		}
	}
	return false
}

// persistRisk flags the commands that install something that runs after the session (a cron or at job, an enabled service, a login
// item, a lingering user manager, a global git setting that runs a program) and those that print a stored credential. nil when the
// command is neither.
func persistRisk(name string, args []string) *riskInfo {
	sub, rest := firstOperand(args)
	switch name {
	case "crontab":
		// crontab -l lists; anything else (a file, -, -e, -r) replaces or edits the table
		if len(args) > 0 && onlyFlags(args, "-l", "-u") {
			return nil
		}
		return &riskInfo{why: "crontab installs a job that runs after this session"}
	case "at", "batch":
		if hasShort(args, "lrd") || hasLong(args, "--list") { // at -l lists, -r/-d removes
			return nil
		}
		return &riskInfo{why: name + " schedules a command that runs after this session"}
	case "systemctl":
		switch sub {
		case "enable", "reenable", "link", "edit", "preset", "preset-all", "add-wants", "add-requires", "set-property", "revert", "unmask":
			return &riskInfo{why: "systemctl " + sub + " installs or changes a service that outlives this session"}
		}
	case "systemd-run":
		return &riskInfo{why: "systemd-run starts a unit outside this session, where it is not stopped with it"}
	case "launchctl":
		switch sub {
		case "load", "bootstrap", "submit", "enable":
			return &riskInfo{why: "launchctl " + sub + " installs a job that outlives this session"}
		}
	case "loginctl":
		if sub == "enable-linger" {
			return &riskInfo{why: "loginctl enable-linger keeps the user's services running after logout"}
		}
	case "schtasks":
		if hasLong(args, "/create", "/Create", "/CREATE") {
			return &riskInfo{why: "schtasks /create schedules a task that runs after this session"}
		}
	case "gh":
		if sub == "auth" {
			s2, r2 := firstOperand(rest)
			if s2 == "token" || s2 == "status" && (hasShort(r2, "t") || hasLong(r2, "--show-token")) {
				return credentialPrint("gh auth " + s2)
			}
		}
	case "gcloud":
		for _, a := range args {
			if a == "print-access-token" || a == "print-identity-token" {
				return credentialPrint("gcloud " + a)
			}
		}
	case "az":
		if sub == "account" {
			if s2, _ := firstOperand(rest); s2 == "get-access-token" {
				return credentialPrint("az account get-access-token")
			}
		}
	case "aws":
		if sub == "configure" {
			s2, r2 := firstOperand(rest)
			if s2 == "export-credentials" {
				return credentialPrint("aws configure export-credentials")
			}
			if k, _ := firstOperand(r2); s2 == "get" && (strings.Contains(k, "secret") || strings.Contains(k, "token") || strings.Contains(k, "access_key")) {
				return credentialPrint("aws configure get " + k)
			}
		}
	case "security":
		if (sub == "find-generic-password" || sub == "find-internet-password") && hasShort(rest, "wg") {
			return credentialPrint("security " + sub)
		}
	case "secret-tool":
		if sub == "lookup" {
			return credentialPrint("secret-tool lookup")
		}
	case "op":
		if sub == "read" || hasLong(rest, "--reveal") {
			return credentialPrint("op " + sub)
		}
	}
	if strings.HasPrefix(name, "docker-credential-") && sub == "get" {
		return credentialPrint(name + " get")
	}
	return nil
}

func credentialPrint(what string) *riskInfo {
	return &riskInfo{why: what + " prints a stored credential into the transcript; let the tool that holds it use it (gh api, git push)"}
}

// gitPersistRisk flags git config that sets, outside one repository (--global, --system, or a --file), a key that makes git run a
// program or fetch from elsewhere for every repository afterwards, and git credential fill, which prints a stored credential. args
// are git's after the subcommand.
func gitPersistRisk(sub string, rest []string) *riskInfo {
	switch sub {
	case "credential":
		if s, _ := firstOperand(rest); s == "fill" {
			return credentialPrint("git credential fill")
		}
		return nil
	case "config":
	default:
		return nil
	}
	if !hasLong(rest, "--global", "--system", "--file") && !hasShort(rest, "f") {
		return nil
	}
	key := ""
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--file" || a == "-f" || a == "--type" || a == "--default" || a == "--comment" || a == "--value":
			i++
		case strings.HasPrefix(a, "-"):
			switch a {
			case "--get", "--get-all", "--get-regexp", "--get-urlmatch", "--list", "-l", "--unset", "--unset-all", "--remove-section", "--rename-section":
				return nil
			}
		case key == "" && (a == "get" || a == "list" || a == "unset" || a == "remove-section" || a == "rename-section"):
			return nil
		case key == "" && a == "set":
		case key == "":
			key = strings.ToLower(a)
		}
	}
	if !gitExecKey(key) {
		return nil
	}
	return &riskInfo{why: "git config sets " + key + " outside this repository: git runs or fetches what it names in every repository afterwards",
		markers: []string{"--global", "--system", "--file", "-f"}}
}

// gitExecKey reports whether a git configuration key names a program git runs, a file it includes, or where it fetches from.
func gitExecKey(key string) bool {
	switch key {
	case "core.hookspath", "core.sshcommand", "core.fsmonitor", "core.pager", "core.editor", "core.askpass", "core.gitproxy",
		"sequence.editor", "diff.external", "gpg.program", "include.path", "web.browser", "credential.helper":
		return true
	}
	for _, p := range []string{"alias.", "includeif.", "pager.", "filter.", "credential."} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	segs := strings.Split(key, ".")
	last := segs[len(segs)-1]
	switch {
	case segs[0] == "url" && (last == "insteadof" || last == "pushinsteadof"):
		return true
	case (segs[0] == "diff" || segs[0] == "merge" || segs[0] == "gpg" || segs[0] == "difftool" || segs[0] == "mergetool") &&
		(last == "textconv" || last == "command" || last == "driver" || last == "program" || last == "cmd"):
		return true
	}
	return false
}

// firstOperand returns the first argument that is not an option, and those after it.
func firstOperand(args []string) (string, []string) {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a, args[i+1:]
		}
	}
	return "", nil
}

// onlyFlags reports whether every argument is one of flags, or the operand of -u (crontab -u bob -l).
func onlyFlags(args []string, flags ...string) bool {
	for i := 0; i < len(args); i++ {
		ok := false
		for _, f := range flags {
			ok = ok || args[i] == f
		}
		if !ok {
			return false
		}
		if args[i] == "-u" {
			i++
		}
	}
	return true
}
