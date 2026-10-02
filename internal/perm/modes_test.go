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
