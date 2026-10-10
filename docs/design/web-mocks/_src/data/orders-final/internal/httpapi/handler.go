// Package httpapi serves the orders over HTTP.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"example.com/orders-api/orders"
)

// Handler serves GET /orders?page=1&size=20.
type Handler struct{ Store *orders.Store }

// ServeHTTP answers with one page of orders as JSON.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("size"))
	if size == 0 {
		size = 20
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"orders": h.Store.List(page, size),
		"pages":  h.Store.Pages(size),
	})
}
