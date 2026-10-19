package plugin

// The needs of the built-in plugins (needs.go): what each program is, where
// its installers usually put it, and the package-manager commands that install
// it. Versions of tool servers are pinned, so a later session runs the same
// server the person installed.

const (
	blenderMCPVersion = "mcp-for-blender@2.2.0"
	reaVersion        = "rea-agents@6.7.0"
)

var (
	needBlender = Need{ID: "blender", Title: "Blender", Homepage: "https://www.blender.org",
		Description: "the 3D suite the Blender skills drive (renders, models, GLB exports)",
		Commands:    []string{"blender"}, VersionArgs: []string{"--version"},
		Paths: map[string][]string{
			"darwin":  {"/Applications/Blender.app/Contents/MacOS/Blender", "~/Applications/Blender.app/Contents/MacOS/Blender"},
			"linux":   {"/snap/bin/blender", "/opt/blender*/blender", "~/.local/share/flatpak/exports/bin/org.blender.Blender", "/var/lib/flatpak/exports/bin/org.blender.Blender"},
			"windows": {"${ProgramFiles}/Blender Foundation/Blender*/blender.exe"},
		},
		Install: []InstallMethod{
			{ID: "brew", Title: "Homebrew", OS: []string{"darwin"}, Run: []string{"brew", "install", "--cask", "blender"}},
			{ID: "winget", Title: "winget", OS: []string{"windows"}, Run: []string{"winget", "install", "--id", "BlenderFoundation.Blender", "-e"}},
			{ID: "flatpak", Title: "Flatpak (Flathub)", OS: []string{"linux"}, Run: []string{"flatpak", "install", "--user", "-y", "flathub", "org.blender.Blender"}},
		}}
	needUV = Need{ID: "uv", Title: "uv", Homepage: "https://docs.astral.sh/uv/",
		Description: "the Python tool runner that starts the Blender tool server (uvx)",
		Commands:    []string{"uv", "uvx"}, VersionArgs: []string{"--version"},
		Paths: map[string][]string{
			"unix":    {"~/.local/bin/uv", "~/.cargo/bin/uv", "/opt/homebrew/bin/uv"},
			"windows": {"${USERPROFILE}/.local/bin/uv.exe"},
		},
		Install: []InstallMethod{
			{ID: "brew", Title: "Homebrew", OS: []string{"darwin"}, Run: []string{"brew", "install", "uv"}},
			{ID: "winget", Title: "winget", OS: []string{"windows"}, Run: []string{"winget", "install", "--id", "astral-sh.uv", "-e"}},
			{ID: "pipx", Title: "pipx", Run: []string{"pipx", "install", "uv"}},
		}}
	needBlenderMCP = Need{ID: "blender-mcp", Title: "Blender tool server", Homepage: "https://github.com/ahujasid/mcp-for-blender",
		Description: "lets agents drive a running Blender through tools (MCP); the Blender skills also work without it, through blender --background",
		MCP:         &NeedMCP{Name: "blender", Command: "uvx", Args: []string{blenderMCPVersion}},
		Install: []InstallMethod{
			{ID: "addon", Title: "Install the Blender add-on and connect", Run: []string{"uvx", blenderMCPVersion, "install-addon"}, Connect: true,
				Note: "then in Blender enable the add-on (Edit > Preferences > Add-ons), open the BlenderMCP tab of the 3D view's sidebar and click Connect"},
			{ID: "connect", Title: "Connect only (the add-on is already installed)", Connect: true},
		}}
	needNode = Need{ID: "node", Title: "Node.js", Homepage: "https://nodejs.org",
		Description: "runs REA and the npx-based tools the reverse-engineering skills use",
		Commands:    []string{"node", "npm", "npx"}, VersionArgs: []string{"--version"},
		Paths: map[string][]string{
			"unix":    {"/opt/homebrew/bin/node", "/usr/local/bin/node"},
			"windows": {"${ProgramFiles}/nodejs/node.exe"},
		},
		Install: []InstallMethod{
			{ID: "brew", Title: "Homebrew", OS: []string{"darwin"}, Run: []string{"brew", "install", "node"}},
			{ID: "winget", Title: "winget", OS: []string{"windows"}, Run: []string{"winget", "install", "--id", "OpenJS.NodeJS.LTS", "-e"}},
		}}
	needREA = Need{ID: "rea", Title: "REA", Homepage: "https://github.com/morluto/rea",
		Description: "the reverse-engineering workbench's command line (rea), for binaries, packages and app bundles",
		Commands:    []string{"rea"},
		Install: []InstallMethod{
			{ID: "npm", Title: "npm (global)", Run: []string{"npm", "install", "-g", reaVersion}},
		}}
	needREAMCP = Need{ID: "rea-mcp", Title: "REA tool server", Homepage: "https://github.com/morluto/rea",
		Description: "REA's evidence-recording tools for agents (MCP)",
		MCP:         &NeedMCP{Name: "rea", Command: "npx", Args: []string{"-y", reaVersion, "mcp"}},
		Install: []InstallMethod{
			{ID: "connect", Title: "Connect", Connect: true},
		}}
	needGH = Need{ID: "gh", Title: "GitHub CLI", Homepage: "https://cli.github.com", Required: true,
		Description: "opens pull requests, watches CI and merges for GitHub delivery; sign in with gh auth login",
		Commands:    []string{"gh"}, VersionArgs: []string{"--version"},
		Paths: map[string][]string{
			"unix":    {"/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/home/linuxbrew/.linuxbrew/bin/gh"},
			"windows": {"${ProgramFiles}/GitHub CLI/gh.exe"},
		},
		Install: []InstallMethod{
			{ID: "brew", Title: "Homebrew", OS: []string{"darwin", "linux"}, Run: []string{"brew", "install", "gh"}},
			{ID: "winget", Title: "winget", OS: []string{"windows"}, Run: []string{"winget", "install", "--id", "GitHub.cli", "-e"}},
		}}
)

// builtinNeeds are the needs of each built-in plugin.
var builtinNeeds = map[string][]Need{
	BuiltinSkills: {needBlender, needUV, needBlenderMCP, needNode, needREA, needREAMCP},
	Delivery:      {needGH},
}
