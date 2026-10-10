package catalog

import (
	"fmt"
	"slices"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// favMu makes each change of the favourites (read the list, change it, save it) one step in this process, so two changes at once
// both stay; config.Save serialises the write itself, not the read before it.
var favMu sync.Mutex

// Favorites is the set of starred models of a configuration (models.favorites).
func Favorites(cfg *config.Config) map[string]bool {
	m := map[string]bool{}
	if cfg == nil {
		return m
	}
	for _, r := range cfg.Models.Favorites {
		m[r] = true
	}
	return m
}

// checkRef refuses a reference that does not name a provider and a model.
func checkRef(ref string) error {
	if _, _, ok := config.SplitModelRef(ref); !ok {
		return fmt.Errorf("%q is not a provider/model reference (see `sleipnir models`)", ref)
	}
	return nil
}

// loadFavorites reads the favourites as the commands see them: the configuration under home, with the project of the working
// directory read as untrusted.
func loadFavorites(home string) ([]string, error) {
	cfg, _, err := config.Load(config.LoadOpts{Home: home, UntrustedProject: true})
	if err != nil {
		return nil, err
	}
	return slices.Clone(cfg.Models.Favorites), nil
}

// ToggleFavorite stars ref in the user's configuration under home, or unstars it when it is starred, and reports whether it is
// starred now.
func ToggleFavorite(home, ref string) (bool, error) {
	if err := checkRef(ref); err != nil {
		return false, err
	}
	favMu.Lock()
	defer favMu.Unlock()
	favs, err := loadFavorites(home)
	if err != nil {
		return false, err
	}
	starred := !slices.Contains(favs, ref)
	if starred {
		favs = append(favs, ref)
	} else {
		favs = slices.DeleteFunc(favs, func(x string) bool { return x == ref })
	}
	return starred, SaveFavorites(home, favs)
}

// SetFavorite stars (on) or unstars ref in the user's configuration under home; asking for the state it already has changes nothing
// and writes nothing. It returns the favourites after the change.
func SetFavorite(home, ref string, on bool) ([]string, error) {
	if err := checkRef(ref); err != nil {
		return nil, err
	}
	favMu.Lock()
	defer favMu.Unlock()
	favs, err := loadFavorites(home)
	if err != nil {
		return nil, err
	}
	if slices.Contains(favs, ref) == on {
		return favs, nil
	}
	if on {
		favs = append(favs, ref)
	} else {
		favs = slices.DeleteFunc(favs, func(x string) bool { return x == ref })
	}
	return favs, SaveFavorites(home, favs)
}

// EditFavorites applies `models fav add|rm` to the favourites under home: add appends the references that are not there, rm removes
// them. Every reference is checked first; the error names the first bad one. It returns the favourites after the change.
func EditFavorites(home string, add bool, refs []string) ([]string, error) {
	for _, r := range refs {
		if err := checkRef(r); err != nil {
			return nil, err
		}
	}
	favMu.Lock()
	defer favMu.Unlock()
	favs, err := loadFavorites(home)
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		if add && !slices.Contains(favs, r) {
			favs = append(favs, r)
		}
		if !add {
			favs = slices.DeleteFunc(favs, func(x string) bool { return x == r })
		}
	}
	return favs, SaveFavorites(home, favs)
}

// SaveFavorites writes the favourites into the user's configuration under home (models.favorites), keeping every other setting of
// the file (its comments are not kept: config.Save writes canonical JSON).
func SaveFavorites(home string, favs []string) error {
	return config.Save(config.UserConfigPath(home), map[string]any{"models": map[string]any{"favorites": favs}})
}
