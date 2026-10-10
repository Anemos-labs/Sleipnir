// Command orders-api serves the orders of the shop.
package main

import (
	"log"
	"net/http"

	"example.com/orders-api/internal/httpapi"
	"example.com/orders-api/orders"
)

func main() {
	store := orders.NewStore([]orders.Order{{ID: 1, Customer: "ada", Total: 1200, Paid: true}})
	http.Handle("/orders", &httpapi.Handler{Store: store})
	log.Fatal(http.ListenAndServe("127.0.0.1:8081", nil))
}
