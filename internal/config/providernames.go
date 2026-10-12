package config

// formerProviderNames maps a provider's current name to the name it had before it was renamed. A model
// reference, a --provider flag and a "providers" entry that still use the former name keep working.
var formerProviderNames = map[string]string{"heimdallm": "heimdall"}

// CanonicalProvider returns a provider's current name: the name itself, or, for the former name of a
// renamed provider ("heimdall"), the name it has now ("heimdallm").
func CanonicalProvider(name string) string {
	for now, was := range formerProviderNames {
		if name == was {
			return now
		}
	}
	return name
}

// ProviderEntry returns the configuration's entry for the provider name (taken through CanonicalProvider).
// An entry under the provider's current name wins over one under its former name.
func (c *Config) ProviderEntry(name string) (Provider, bool) {
	if c == nil {
		return Provider{}, false
	}
	name = CanonicalProvider(name)
	if p, ok := c.Providers[name]; ok {
		return p, true
	}
	if was, ok := formerProviderNames[name]; ok {
		p, ok := c.Providers[was]
		return p, ok
	}
	return Provider{}, false
}
