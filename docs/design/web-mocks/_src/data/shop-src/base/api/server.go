// Package api serves the shop's HTTP API.
package api

import (
	"net/http"
)

// Server holds what the handlers share.
type Server struct {
	ledger *orderBook
	people *customerBook
}

// NewServer returns a Server with empty books.
func NewServer() *Server {
	return &Server{
		ledger: newOrderBook(),
		people: newCustomerBook(),
	}
}

// Handler returns the API and the static page as one http.Handler.
func (s *Server) Handler() http.Handler { return s.routes() }

// routes registers every endpoint.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /orders", s.orders)
	mux.HandleFunc("POST /orders", s.createOrder)
	mux.HandleFunc("GET /orders/{id}", s.order)
	mux.HandleFunc("POST /orders/{id}/cancel", s.cancelOrder)
	mux.HandleFunc("GET /customers", s.customers)
	mux.HandleFunc("POST /customers", s.createCustomer)
	mux.HandleFunc("GET /customers/{id}", s.customer)
	mux.HandleFunc("GET /shipments/{id}", s.shipment)
	mux.HandleFunc("POST /shipments", s.createShipment)
	mux.HandleFunc("GET /stock/{sku}", s.stock)
	mux.Handle("GET /", http.FileServer(http.Dir("web")))
	return mux
}

// health reports that the process is up.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
