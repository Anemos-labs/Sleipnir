package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
)

// Customer is someone who can place an order.
type Customer struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

// customerBook keeps the customers in memory.
type customerBook struct {
	mu   sync.Mutex
	rows []Customer
}

// newCustomerBook returns an empty book.
func newCustomerBook() *customerBook { return &customerBook{} }

// customers lists every customer.
func (s *Server) customers(w http.ResponseWriter, r *http.Request) {
	s.people.mu.Lock()
	defer s.people.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"customers": s.people.rows})
}

// createCustomer adds a customer from a JSON body.
func (s *Server) createCustomer(w http.ResponseWriter, r *http.Request) {
	var c Customer
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil || c.Email == "" {
		writeError(w, http.StatusBadRequest, "a customer needs an email")
		return
	}
	s.people.mu.Lock()
	c.ID = "cus-" + strconv.Itoa(len(s.people.rows)+1)
	s.people.rows = append(s.people.rows, c)
	s.people.mu.Unlock()
	writeJSON(w, http.StatusCreated, c)
}

// customer returns one customer by id.
func (s *Server) customer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.people.mu.Lock()
	defer s.people.mu.Unlock()
	for _, c := range s.people.rows {
		if c.ID == id {
			writeJSON(w, http.StatusOK, c)
			return
		}
	}
	writeError(w, http.StatusNotFound, "no such customer")
}
