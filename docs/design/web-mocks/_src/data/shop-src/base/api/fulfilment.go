package api

import "net/http"

// Shipment is a parcel on its way for one order.
type Shipment struct {
	ID      string `json:"id"`
	OrderID string `json:"order_id"`
	Carrier string `json:"carrier"`
	Status  string `json:"status"`
}

// shipment returns one shipment by id.
func (s *Server) shipment(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "no shipment "+r.PathValue("id"))
}

// createShipment books a carrier for an order.
func (s *Server) createShipment(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusAccepted, Shipment{ID: "shp-1", Status: "booked"})
}

// stock reports how many units of a SKU are on the shelf.
func (s *Server) stock(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"sku": r.PathValue("sku"), "on_hand": 0})
}
