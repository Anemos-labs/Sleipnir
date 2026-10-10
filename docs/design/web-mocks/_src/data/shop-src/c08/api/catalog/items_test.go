package catalog

import (
	"fmt"
	"testing"
)

func seed(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{ID: fmt.Sprintf("itm-%03d", i+1), PriceCents: int64(100 * (i + 1))}
	}
	return out
}

func TestList(t *testing.T) {
	s := New(seed(48))
	cases := []struct {
		name                     string
		page, size               int
		wantPage, wantPages, len int
	}{
		{"first page", 1, 12, 1, 4, 12},
		{"last page", 4, 12, 4, 4, 12},
		{"page 0 is page 1", 0, 12, 1, 4, 12},
		{"a negative page is page 1", -3, 12, 1, 4, 12},
		{"a page past the end is the last page", 9, 12, 4, 4, 12},
		{"size 0 is size 1", 1, 0, 1, 48, 1},
		{"a size over the maximum is clamped", 1, 500, 1, 1, 48},
		{"the last page may be short", 3, 20, 3, 3, 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := s.List(c.page, c.size)
			if got.Page != c.wantPage || got.Pages != c.wantPages || len(got.Items) != c.len || got.Total != 48 {
				t.Fatalf("List(%d, %d): page %d of %d, %d items, total %d", c.page, c.size, got.Page, got.Pages, len(got.Items), got.Total)
			}
		})
	}
}

func TestListEmpty(t *testing.T) {
	if got := New(nil).List(1, 12); got.Page != 1 || got.Pages != 0 || len(got.Items) != 0 {
		t.Fatalf("empty store: %+v", got)
	}
}
