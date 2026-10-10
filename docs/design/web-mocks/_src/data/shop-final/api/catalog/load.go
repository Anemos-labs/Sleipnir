package catalog

import (
	"encoding/json"
	"os"
)

// Load reads the seed file: a JSON array of items, prices in cents.
func Load(path string) ([]Item, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []Item
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
