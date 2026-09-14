package order

import "testing"

func TestTotal(t *testing.T) {
	svc := NewService(10)
	o := &Order{}
	if svc.Total(o).Cents != 0 {
		t.Fatal("expected zero")
	}
	_ = o.Key()
}
