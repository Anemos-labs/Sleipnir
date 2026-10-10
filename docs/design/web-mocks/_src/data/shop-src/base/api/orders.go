package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Line is one item of an order, priced in cents.
type Line struct {
	ItemID     string `json:"item_id"`
	Qty        int    `json:"qty"`
	PriceCents int64  `json:"price_cents"`
}

// Order is one purchase.
type Order struct {
	ID        string    `json:"id"`
	Customer  string    `json:"customer"`
	Lines     []Line    `json:"lines"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// orderBook keeps the orders in memory, oldest first.
type orderBook struct {
	mu   sync.Mutex
	rows []Order
}

// newOrderBook returns an empty book.
func newOrderBook() *orderBook { return &orderBook{} }

// after returns up to limit orders that follow the order with id cursor (the first ones when cursor is empty), and the
// cursor to ask for the next page ("" at the end).
func (b *orderBook) after(cursor string, limit int) ([]Order, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	start := 0
	if cursor != "" {
		for i, o := range b.rows {
			if o.ID == cursor {
				start = i + 1
				break
			}
		}
	}
	end := min(start+limit, len(b.rows))
	next := ""
	if end < len(b.rows) {
		next = b.rows[end-1].ID
	}
	return append([]Order(nil), b.rows[start:end]...), next
}

// orders lists orders with a cursor: GET /orders?cursor=ord-0007&limit=20.
func (s *Server) orders(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 100 {
		limit = 20
	}
	rows, next := s.ledger.after(r.URL.Query().Get("cursor"), limit)
	writeJSON(w, http.StatusOK, map[string]any{"orders": rows, "next": next})
}

// createOrder records an order from a JSON body.
func (s *Server) createOrder(w http.ResponseWriter, r *http.Request) {
	var o Order
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
		writeError(w, http.StatusBadRequest, "bad order: "+err.Error())
		return
	}
	s.ledger.mu.Lock()
	o.ID = "ord-" + strconv.Itoa(len(s.ledger.rows)+1)
	o.Status, o.CreatedAt = "open", time.Now().UTC()
	s.ledger.rows = append(s.ledger.rows, o)
	s.ledger.mu.Unlock()
	writeJSON(w, http.StatusCreated, o)
}

// order returns one order by id.
func (s *Server) order(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	for _, o := range s.ledger.rows {
		if o.ID == id {
			writeJSON(w, http.StatusOK, o)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no such order")
}

// cancelOrder marks an open order cancelled.
func (s *Server) cancelOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.ledger.mu.Lock()
	defer s.ledger.mu.Unlock()
	for i, o := range s.ledger.rows {
		if o.ID == id && o.Status == "open" {
			s.ledger.rows[i].Status = "cancelled"
			writeJSON(w, http.StatusOK, s.ledger.rows[i])
			return
		}
	}
	writeError(w, http.StatusNotFound, "no open order with that id")
}
