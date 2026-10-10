package cart

import "testing"

func TestTotalIsExactInCents(t *testing.T) {
	c := New()
	c.Add("itm-001", 10, 1)
	c.Add("itm-002", 20, 1)
	if got := c.Total(); got != 30 {
		t.Fatalf("Total() = %d, want 30", got)
	}
}

func TestTotalMultipliesByQuantity(t *testing.T) {
	c := New()
	c.Add("itm-001", 850, 3)
	if got := c.Total(); got != 2550 {
		t.Fatalf("Total() = %d, want 2550", got)
	}
}

func TestRemove(t *testing.T) {
	c := New()
	c.Add("itm-001", 100, 1)
	c.Add("itm-002", 200, 2)
	if !c.Remove("itm-001") {
		t.Fatal("Remove(itm-001) = false, want true")
	}
	if c.Remove("itm-001") {
		t.Fatal("Remove(itm-001) twice = true, want false")
	}
	if got := c.Total(); got != 400 {
		t.Fatalf("Total() after Remove = %d, want 400", got)
	}
}
