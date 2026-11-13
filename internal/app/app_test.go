package app

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, body := range []string{`{"customer_id":"customer-demo","unknown":1}`, `{"customer_id":"customer-demo"} {}`} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(body))
		var q Checkout
		if decode(r, &q) == nil {
			t.Fatalf("decode accepted invalid body %s", body)
		}
	}
}
func TestDecodeAcceptsStrictCheckout(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"customer_id":"customer-demo","sku":"SKU-001","quantity":1,"idempotency_key":"demo-1"}`))
	var q Checkout
	if e := decode(r, &q); e != nil {
		t.Fatal(e)
	}
	if q.Key != "demo-1" || q.Quantity != 1 {
		t.Fatalf("decoded wrong request: %#v", q)
	}
}
func TestValidIdempotencyKeys(t *testing.T) {
	for _, k := range []string{"demo-1", "healthy-1730000000", "x.y_z"} {
		if !validKey(k) {
			t.Errorf("rejected %q", k)
		}
	}
	for _, k := range []string{"", "bad/key", "contains space", strings.Repeat("x", 129)} {
		if validKey(k) {
			t.Errorf("accepted %q", k)
		}
	}
}
