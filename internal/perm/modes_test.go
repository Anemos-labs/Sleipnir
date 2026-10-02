package perm

import "testing"

// bypass gives full control without asking, except about the very dangerous: the high-risk class and a recursive delete of a path that is only known
// when the command runs. yolo never asks: it allows those too, and refuses (instead of asking) what a rule says to ask about.
func TestBypassAsksAboutTheVeryDangerousAndYoloAsksNothing(t *testing.T) {
	f := newFixture(t)
	runCases(t, f, []tc{
		{name: "bypass allows an ordinary command", mode: ModeBypass, req: bash("go test ./..."), want: "allow", why: "bypass"},
		{name: "bypass allows a build with an install", mode: ModeBypass, req: bash("npm install && npm run build"), want: "allow"},
		{name: "bypass asks about rm -rf on a variable", mode: ModeBypass, req: bash(`rm -rf "$DIR"`), want: "ask", why: "only known when the command runs"},
		{name: "bypass asks about rm -rf on a variable path", mode: ModeBypass, req: bash(`rm -rf $BUILD/out`), want: "ask"},
		{name: "bypass allows rm -rf of a plain build directory", mode: ModeBypass, req: bash("rm -rf build"), want: "allow"},
		{name: "bypass asks about sudo", mode: ModeBypass, req: bash("sudo apt-get install x"), want: "ask", why: "high risk"},
		{name: "bypass asks about a forced push to main", mode: ModeBypass, req: bash("git push --force origin main"), want: "ask"},
		{name: "yolo allows an ordinary command", mode: ModeYolo, req: bash("go test ./..."), want: "allow", why: "yolo"},
		{name: "yolo allows rm -rf on a variable", mode: ModeYolo, req: bash(`rm -rf "$DIR"`), want: "allow"},
		{name: "yolo allows sudo", mode: ModeYolo, req: bash("sudo apt-get install x"), want: "allow"},
		{name: "yolo still refuses what a deny rule names", mode: ModeYolo, deny: []string{"Bash(rm:*)"}, req: bash("rm -rf x"), want: "deny"},
		{name: "yolo refuses instead of asking for an ask rule", mode: ModeYolo, ask: []string{"Bash(git push:*)"}, req: bash("git push origin dev"), want: "deny", why: "yolo mode never asks"},
		{name: "an ordinary session still asks for an ask rule", ask: []string{"Bash(git push:*)"}, req: bash("git push origin dev"), want: "ask"},
	})
}

// A mode is a valid mode, and yolo is the most permissive.
func TestYoloIsAModeAndTheMostPermissiveOne(t *testing.T) {
	if !validMode(ModeYolo) || modeRank(ModeYolo) <= modeRank(ModeBypass) {
		t.Errorf("yolo: valid %v, rank %d against bypass %d", validMode(ModeYolo), modeRank(ModeYolo), modeRank(ModeBypass))
	}
	if !free(ModeBypass) || !free(ModeYolo) || free(ModeDefault) || free(ModeAcceptEdits) || free(ModePlan) {
		t.Error("free is bypass and yolo, and no other mode")
	}
}

// The third answer of a question about a build or test command allows the builds and tests of the project for the session: the next worker's
// go build, gofmt -w or npm test is not asked, and a command the set does not cover still is.
func TestTheThirdAnswerAllowsBuildsAndTestsForTheSession(t *testing.T) {
	f := newFixture(t)
	var offers []bool
	rec := &promptRecorder{answer: func(_ int, r Request) Decision {
		offers = append(offers, r.OffersTests)
		return Decision{Allow: true, Remember: ScopeSession, Preset: PresetTests}
	}}
	e := askEngine(t, f, Config{}, rec.prompt)
	e.Check(bg, f.request(bash("go test ./a")))
	if len(offers) != 1 || !offers[0] {
		t.Fatalf("the question about go test offers the third answer: %v", offers)
	}
	for _, cmd := range []string{"go build ./b", "gofmt -w x.go", "npm test", "cargo test", "pytest -q"} {
		if d := e.Check(bg, f.request(bash(cmd))); !d.Allow || rec.count() != 1 {
			t.Errorf("%s after the third answer: %+v, %d questions", cmd, d, rec.count())
		}
	}
	e.Check(bg, f.request(bash("curl https://example.com/x")))
	if rec.count() != 2 || len(offers) != 2 || offers[1] {
		t.Errorf("a command outside the set asks, and does not offer it: %d questions, offers %v", rec.count(), offers)
	}
}
