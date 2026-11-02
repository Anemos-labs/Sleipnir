package catalog

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A model chosen for a running session (the web page's Models, `/model` in the chat) lasts as long as that session: the next start
// reads models.default and models.roles from the configuration again. The functions below keep a choice there, in the user's own
// file, and say where the value a start would read comes from, so that a choice that a project file, SLEIPNIR_MODEL or a flag
// outranks is not taken for one that sticks.

// roleNameRE is the shape of a role name a saved default may name: the built-in roles and those of agent definitions.
var roleNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// DefaultKey is the configuration key that holds the default model of role: models.default for "", "default" and "manager" (the
// manager runs on the team's model, which is models.default, unless models.roles.manager names another), models.roles.<role> for
// any other role.
func DefaultKey(role string) string {
	switch role = strings.TrimSpace(role); role {
	case "", "default", "manager":
		return "models.default"
	}
	return "models.roles." + role
}

// ValidRole says whether role can name a saved default: "", "default", "manager", or the name of a role (lower case letters, digits,
// - and _, at most 40).
func ValidRole(role string) bool {
	role = strings.TrimSpace(role)
	return DefaultKey(role) == "models.default" || roleNameRE.MatchString(role)
}

// SavedDefault is where the default model of a role comes from in a configuration: the value a new session reads, the source of
// the layer that set it (a file path, "env:NAME", or "" when no layer did), and its kind (user, project, local, env, overrides).
type SavedDefault struct {
	Key, Ref, Source, Kind string
}

// DefaultOf reads the default model of role from cfg and rep (config.Load's result). For the manager, an entry under
// models.roles.manager wins over models.default, as it does when a team starts.
func DefaultOf(cfg *config.Config, rep *config.Report, role string) SavedDefault {
	key := DefaultKey(role)
	if strings.TrimSpace(role) == "manager" && cfg != nil && cfg.Models.Roles["manager"] != "" {
		key = "models.roles.manager"
	}
	d := SavedDefault{Key: key}
	if cfg != nil {
		if key == "models.default" {
			d.Ref = cfg.Models.Default
		} else {
			d.Ref = cfg.Models.Roles[strings.TrimPrefix(key, "models.roles.")]
		}
	}
	if rep != nil {
		d.Source, d.Kind = rep.Origins[key], rep.KindOf(key)
	}
	return d
}

// SaveDefault keeps ref as the default model of role in the user's configuration under home (DefaultKey says which key), keeping
// every other setting of the file (its comments are not kept: config.Save writes canonical JSON). For the manager it also removes
// a models.roles.manager entry of the user's file, which would otherwise outrank the default it writes. It returns the path it wrote.
func SaveDefault(home, role, ref string) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", fmt.Errorf("no home directory: the user configuration cannot be located")
	}
	ref, role = strings.TrimSpace(ref), strings.TrimSpace(role)
	if err := checkRef(ref); err != nil {
		return "", err
	}
	key := DefaultKey(role)
	if !ValidRole(role) {
		return "", fmt.Errorf("%q is not a role name", role)
	}
	models := map[string]any{}
	if key == "models.default" {
		models["default"] = ref
		if role == "manager" {
			if cfg, _, err := config.Load(config.LoadOpts{Home: home, Cwd: home, UntrustedProject: true}); err == nil && cfg.Models.Roles["manager"] != "" {
				models["roles"] = map[string]any{"manager": nil}
			}
		}
	} else {
		models["roles"] = map[string]any{role: ref}
	}
	path := config.UserConfigPath(home)
	return path, config.Save(path, map[string]any{"models": models})
}
