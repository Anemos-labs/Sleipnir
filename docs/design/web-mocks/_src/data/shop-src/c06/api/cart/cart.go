// Package cart keeps what a customer is about to buy.
package cart

// Line is one item in a cart.
type Line struct {
	ID         string
	Qty        int
	PriceCents int64
	Price      float64 // dollars, kept for the receipt printer
}

// Cart is a customer's basket.
type Cart struct {
	lines []Line
}

// New returns an empty cart.
func New() *Cart { return &Cart{} }

// Add puts qty of an item, priced in cents, into the cart.
func (c *Cart) Add(id string, price int64, qty int) {
	c.lines = append(c.lines, Line{ID: id, Qty: qty, PriceCents: price, Price: float64(price) / 100})
}

// Lines returns the cart's lines.
func (c *Cart) Lines() []Line { return append([]Line(nil), c.lines...) }

// Total is what the cart costs, in cents.
func (c *Cart) Total() int64 {
	var total int64
	for _, l := range c.lines {
		total += l.PriceCents * int64(l.Qty)
	}
	return total
}
