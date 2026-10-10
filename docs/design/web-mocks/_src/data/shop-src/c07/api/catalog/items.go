package catalog

import "sort"

// MaxSize is the largest page a client may ask for.
const MaxSize = 48

// Item is one thing the shop sells. Prices are in cents.
type Item struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
}

// Page is one page of the catalogue, counted from 1.
type Page struct {
	Items []Item `json:"items"`
	Page  int    `json:"page"`
	Pages int    `json:"pages"`
	Total int    `json:"total"`
}

// Store holds the catalogue in memory, sorted by ID.
type Store struct{ rows []Item }

// New returns a Store over rows, sorted by ID.
func New(rows []Item) *Store {
	s := &Store{rows: append([]Item(nil), rows...)}
	sort.Slice(s.rows, func(i, j int) bool { return s.rows[i].ID < s.rows[j].ID })
	return s
}

// List returns page number page (from 1) of at most size items. A page below 1 is page 1,
// a page past the end is the last page, and a size outside 1..MaxSize is clamped.
func (s *Store) List(page, size int) Page {
	size = min(max(size, 1), MaxSize)
	pages := (len(s.rows) + size - 1) / size
	page = min(max(page, 1), max(pages, 1))
	lo := (page - 1) * size
	hi := min(lo+size, len(s.rows))
	return Page{Items: s.rows[lo:hi], Page: page, Pages: pages, Total: len(s.rows)}
}
