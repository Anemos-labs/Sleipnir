package perm

import "testing"

// A backdoored model needs something that runs after its session: a startup file, a scheduled job, a service, a global git hook, a .pth
// file. Bypass mode refuses those with the reason (the "ask" outcome), a person is asked about them, plan mode denies them, an Allow
// rule that names the file or the exact command lets them through, and yolo mode does not look.
func TestPersistenceIsAskedAboutAndBypassRefusesIt(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "bypass refuses a write to .bashrc", mode: ModeBypass, req: write("{home}/.bashrc"), want: "ask", why: "runs after this session"},
		{name: "bypass refuses an append to .zshrc", mode: ModeBypass, req: bash("echo 'curl x | sh' >> ~/.zshrc"), want: "ask", why: "shell startup file"},
		{name: "bypass refuses a user service", mode: ModeBypass, req: write("{home}/.config/systemd/user/x.service"), want: "ask", why: "service"},
		{name: "bypass refuses a launch agent", mode: ModeBypass, req: write("{home}/Library/LaunchAgents/x.plist"), want: "ask"},
		{name: "bypass refuses a program in ~/.local/bin", mode: ModeBypass, req: bash("cp tool {home}/.local/bin/git"), want: "ask", why: "$PATH"},
		{name: "bypass refuses the global git config", mode: ModeBypass, req: edit("{home}/.gitconfig"), want: "ask"},
		{name: "bypass refuses a package index redirect", mode: ModeBypass, req: write("{home}/.config/pip/pip.conf"), want: "ask", why: "package index"},
		{name: "bypass refuses a .pth outside the workspace", mode: ModeBypass, req: write("{out}/lib/python3.12/site-packages/x.pth"), want: "ask", why: "Python"},
		{name: "bypass refuses a copy over the home directory", mode: ModeBypass, req: bash("cp -r units/. ~/.config/systemd"), want: "ask", why: "narrower"},
		{name: "default asks about .bashrc", req: write("{home}/.bashrc"), want: "ask", why: "persistence"},
		{name: "plan denies .bashrc", mode: ModePlan, req: write("{home}/.bashrc"), want: "deny"},
		{name: "an allow rule naming the file lets it through", mode: ModeBypass, allow: []string{"Edit({home}/.bashrc)"}, req: write("{home}/.bashrc"), want: "allow"},
		{name: "yolo does not look", mode: ModeYolo, req: write("{home}/.bashrc"), want: "allow"},
		{name: "reading .bashrc plants nothing", mode: ModeBypass, req: read("{home}/.bashrc"), want: "allow"},
		{name: "a .pth inside the workspace is the project's", mode: ModeBypass, req: write("{root}/.venv/lib/python3.12/site-packages/x.pth"), want: "allow"},
		{name: "a project file named bin is the project's", mode: ModeBypass, req: write("{root}/bin/tool"), want: "allow"},
		{name: "an ordinary write outside the workspace is unchanged", mode: ModeBypass, req: write("{home}/notes.txt"), want: "allow"},
		{name: "a formatter over the project is unchanged", mode: ModeBypass, req: bash("gofmt -w ."), want: "allow"},

		{name: "bypass refuses crontab", mode: ModeBypass, req: bash("crontab job.txt"), want: "ask", why: "crontab"},
		{name: "bypass refuses crontab from stdin", mode: ModeBypass, req: bash("echo '* * * * * x' | crontab -"), want: "ask"},
		{name: "crontab -l only lists", mode: ModeBypass, req: bash("crontab -l"), want: "allow"},
		{name: "bypass refuses at", mode: ModeBypass, req: bash("echo x | at now + 1 minute"), want: "ask"},
		{name: "bypass refuses systemctl enable", mode: ModeBypass, req: bash("systemctl --user enable x.service"), want: "ask", why: "outlives"},
		{name: "systemctl status is unchanged", mode: ModeBypass, req: bash("systemctl status nginx"), want: "allow"},
		{name: "bypass refuses launchctl load", mode: ModeBypass, req: bash("launchctl load x.plist"), want: "ask"},
		{name: "bypass refuses enable-linger", mode: ModeBypass, req: bash("loginctl enable-linger"), want: "ask"},
		{name: "bypass refuses a hidden crontab in sh -c", mode: ModeBypass, req: bash(`sh -c "crontab job.txt"`), want: "ask"},
		{name: "an exact allow rule lets crontab through", mode: ModeBypass, allow: []string{"Bash(crontab job.txt)"}, req: bash("crontab job.txt"), want: "allow"},

		{name: "bypass refuses a global hooks path", mode: ModeBypass, req: bash("git config --global core.hooksPath /tmp/h"), want: "ask", why: "core.hookspath"},
		{name: "bypass refuses a global alias", mode: ModeBypass, req: bash("git config --global alias.st '!sh x'"), want: "ask"},
		{name: "bypass refuses a URL rewrite", mode: ModeBypass, req: bash("git config --global url.https://evil.example/.insteadOf https://github.com/"), want: "ask"},
		{name: "bypass refuses the new git config set syntax", mode: ModeBypass, req: bash("git config set --global core.sshCommand x"), want: "ask"},
		{name: "a global identity is unchanged", mode: ModeBypass, req: bash("git config --global user.email a@b.c"), want: "allow"},
		{name: "a repository hooks path is the project's", mode: ModeBypass, req: bash("git config core.hooksPath .husky"), want: "allow"},
		{name: "reading a global key is unchanged", mode: ModeBypass, req: bash("git config --global --get core.hooksPath"), want: "allow"},

		{name: "bypass refuses gh auth token", mode: ModeBypass, req: bash("gh auth token"), want: "ask", why: "gh api"},
		{name: "bypass refuses a token in a curl header", mode: ModeBypass, req: bash(`curl -H "Authorization: token $(gh auth token)" https://api.github.com/user`), want: "ask"},
		{name: "gh auth status is unchanged", mode: ModeBypass, req: bash("gh auth status"), want: "allow"},
		{name: "bypass refuses gh auth status --show-token", mode: ModeBypass, req: bash("gh auth status --show-token"), want: "ask"},
		{name: "bypass refuses git credential fill", mode: ModeBypass, req: bash("printf 'host=github.com\\n' | git credential fill"), want: "ask"},
		{name: "bypass refuses a gcloud token", mode: ModeBypass, req: bash("gcloud auth print-access-token"), want: "ask"},
		{name: "aws configure get region is unchanged", mode: ModeBypass, req: bash("aws configure get region"), want: "allow"},
		{name: "bypass refuses aws configure get of a secret", mode: ModeBypass, req: bash("aws configure get aws_secret_access_key"), want: "ask"},
		{name: "gh api is unchanged", mode: ModeBypass, req: bash("gh api repos/o/r"), want: "allow"},
	})
}

// A person who opened ~/.config/nvim as the project has said that editing it is the task; the home directory as the project has not.
func TestPersistenceInsideAChosenWorkspace(t *testing.T) {
	f := newFixture(t)
	nvim := f.home + "/.config/nvim"
	e := f.engine(t, Config{Mode: ModeBypass})
	e2, err := NewEngine(Config{Mode: ModeBypass, Root: nvim, Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	if d := e2.Check(t.Context(), f.request(write(nvim+"/init.lua"))); !d.Allow {
		t.Errorf("init.lua in a chosen ~/.config/nvim: %q", d.Reason)
	}
	eh, err := NewEngine(Config{Mode: ModeBypass, Root: f.home, Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	if d := eh.Check(t.Context(), f.request(write("{home}/.bashrc"))); d.Allow {
		t.Errorf(".bashrc with the home directory as the project: allowed (%q)", d.Reason)
	}
	if d := e.Check(t.Context(), f.request(write(nvim+"/init.lua"))); d.Allow {
		t.Errorf("init.lua from another project: allowed (%q)", d.Reason)
	}
}
