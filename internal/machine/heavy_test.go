package machine

import "testing"

func TestHeavy(t *testing.T) {
	heavy := []string{
		"go test ./...",
		"cd web && npm run build",
		"npm ci",
		"npm --prefix web run test:unit",
		"pnpm --filter app build",
		"yarn build",
		"npx vitest run",
		"npx playwright test",
		"pnpm exec tsc --noEmit",
		"cargo build --release",
		"cargo test -p core",
		"make -j8",
		"cmake --build build",
		"python -m pytest -q",
		"uv run pytest",
		"pytest tests/",
		"./scripts/check.sh",
		"bash scripts/build.sh",
		"timeout 300 go build ./...",
		"FOO=1 nice -n 5 cargo clippy",
		"docker build -t x .",
		"docker compose build api",
		"docker-compose build",
		"dotnet test",
		"gradle assemble",
		"./gradlew build",
		"go vet ./... && golangci-lint run",
		"bun test",
		"git status && npm test",
	}
	light := []string{
		"ls -la",
		"cat package.json",
		"git diff",
		"rg TODO src",
		"npm run dev",
		"npm view vite version",
		"node server.js",
		"python3 app.py",
		"curl -s localhost:8080",
		"go version",
		"cargo --version",
		"echo build",
		"grep -r test .",
		"./scripts/serve.sh",
		"go mod tidy",
		"go run ./cmd/server",
		"cargo run --release",
		"dotnet run",
		"swift run",
		"docker compose up",
		"docker-compose up -d",
	}
	for _, c := range heavy {
		if !Heavy(c) {
			t.Errorf("%q is not heavy", c)
		}
	}
	for _, c := range light {
		if Heavy(c) {
			t.Errorf("%q is heavy", c)
		}
	}
}
