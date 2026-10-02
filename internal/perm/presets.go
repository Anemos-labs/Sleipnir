package perm

// TestsAllow is the preset of allow rules for the commands that build and test a project (`--allow tests`, and the third answer of the question
// about such a command: "allow builds and tests for this session"). They run the project's own code, as running its tests is meant to, and
// nothing else: no package installs, no downloads, no interpreter given a program of its own.
var TestsAllow = []string{
	"Bash(go test:*)", "Bash(go build:*)", "Bash(go vet:*)", "Bash(gofmt:*)", "Bash(go mod init:*)", "Bash(go mod tidy:*)",
	"Bash(cargo test:*)", "Bash(cargo build:*)", "Bash(cargo check:*)", "Bash(cargo clippy:*)", "Bash(cargo fmt:*)",
	"Bash(npm test:*)", "Bash(npm run test:*)", "Bash(npm run build:*)", "Bash(npm run lint:*)", "Bash(pnpm test:*)", "Bash(yarn test:*)",
	"Bash(node --test:*)",
	"Bash(pytest:*)", "Bash(python -m pytest:*)", "Bash(python3 -m pytest:*)", "Bash(python -m unittest:*)", "Bash(python3 -m unittest:*)",
	"Bash(mvn test:*)", "Bash(mvn -q test:*)", "Bash(gradle test:*)", "Bash(./gradlew test:*)",
	"Bash(dotnet test:*)", "Bash(dotnet build:*)",
	"Bash(make test:*)", "Bash(make check:*)", "Bash(make build:*)", "Bash(make lint:*)", "Bash(ctest:*)",
}

// PresetTests is the name of that set in a Decision.
const PresetTests = "tests"

// addPreset gives the session the rules of a named preset. An unknown name adds nothing.
func (e *Engine) addPreset(name string) {
	if name != PresetTests {
		return
	}
	for _, s := range TestsAllow {
		if r, err := ParseRule(Allow, s); err == nil {
			e.AddRule(ScopeSession, r)
		}
	}
}
