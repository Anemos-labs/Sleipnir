package perm

import "testing"

// The Rust toolchain's own queries read: the versions of cargo, rustc and rustup, cargo metadata --no-deps, rustc --print and
// rustup's listings of what is installed are allowed without a question in the default and accept-edits modes. The forms that
// install, download, resolve dependencies, compile or write a file still ask, and a pipe into a write is judged as before.
func TestRustToolchainQueriesAreReadOnly(t *testing.T) {
	f := newFixture(t)
	reads := []string{
		"cargo --version", "cargo -V", "cargo -vV", "cargo --version --verbose",
		"rustc --version", "rustc -V", "rustc -vV", "rustc --print sysroot", "rustc --print cfg --target x86_64-unknown-linux-gnu",
		"rustc --print=target-list", "rustup --version", "rustup -V", "rustup show", "rustup show active-toolchain", "rustup show home",
		"rustup toolchain list", "rustup toolchain list -v", "rustup target list --installed",
		"rustup target list --installed --toolchain stable", "rustup component list --installed",
		"cargo metadata --no-deps --format-version 1", "cargo metadata --no-deps --format-version=1 --offline",
		"cargo metadata --format-version 1 --no-deps --manifest-path crates/a/Cargo.toml",
		"cargo --version && rustc --version && rustup toolchain list",
		"cargo metadata --no-deps --format-version 1 | jq -r '.packages[].name'",
	}
	asks := []string{
		"rustup toolchain install stable", "rustup toolchain uninstall nightly", "rustup update", "rustup default nightly",
		"rustup target add wasm32-unknown-unknown", "rustup component add clippy", "rustup target list", "rustup component list",
		"rustup show --installed", "rustup toolchain list --toolchain x", "rustup +nightly show", "rustup run stable cargo build",
		"rustup self update", "rustup override set nightly",
		"cargo metadata", "cargo metadata --format-version 1", "cargo metadata --offline --format-version 1",
		"cargo metadata --no-deps --config 'build.rustc-wrapper=\"sh\"'", "cargo metadata --no-deps -Zunstable-options",
		"cargo +nightly metadata --no-deps", "cargo metadata --no-deps --manifest-path /etc/x/Cargo.toml",
		"cargo install ripgrep", "cargo update", "cargo --version --list",
		"rustc main.rs", "rustc --print cfg=out.txt", "rustc --print sysroot main.rs", "rustc --print cfg --target ./spec.json",
		"rustc --print sysroot -C linker=sh", "rustc --print",
	}
	// A write of the output is an edit: default mode asks, and accept-edits allows it inside the workspace.
	writes := []string{
		"cargo metadata --no-deps --format-version 1 > meta.json",
		"cargo metadata --no-deps --format-version 1 | tee meta.json",
	}
	var cases []tc
	for _, c := range writes {
		cases = append(cases, tc{name: "default/" + c, mode: ModeDefault, req: bash(c), want: "ask"},
			tc{name: "accept-edits/" + c, mode: ModeAcceptEdits, req: bash(c), want: "allow"})
	}
	for _, mode := range []Mode{ModeDefault, ModeAcceptEdits} {
		for _, c := range reads {
			cases = append(cases, tc{name: string(mode) + "/" + c, mode: mode, req: bash(c), want: "allow"})
		}
		for _, c := range asks {
			cases = append(cases, tc{name: string(mode) + "/" + c, mode: mode, req: bash(c), want: "ask"})
		}
	}
	runCases(t, f, cases)
}
