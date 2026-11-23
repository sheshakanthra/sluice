package httpx

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequestHashCanonical(t *testing.T) {
	base := RequestHash("POST", "POST /a/{id}", []byte(`{"quantity":2,"seat_nos":[1,2]}`))
	tests := []struct {
		name   string
		method string
		route  string
		body   string
		same   bool
	}{
		{"key order and whitespace", "POST", "POST /a/{id}", "{ \"seat_nos\": [1, 2],\n \"quantity\": 2 }", true},
		{"different value", "POST", "POST /a/{id}", `{"quantity":3,"seat_nos":[1,2]}`, false},
		{"array order matters", "POST", "POST /a/{id}", `{"quantity":2,"seat_nos":[2,1]}`, false},
		{"different route", "POST", "POST /b/{id}", `{"quantity":2,"seat_nos":[1,2]}`, false},
		{"different method", "PUT", "POST /a/{id}", `{"quantity":2,"seat_nos":[1,2]}`, false},
		{"large number kept exact", "POST", "POST /a/{id}", `{"quantity":2.0000000000000001,"seat_nos":[1,2]}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RequestHash(tc.method, tc.route, []byte(tc.body))
			if bytes.Equal(got, base) != tc.same {
				t.Errorf("same = %v, want %v", !tc.same, tc.same)
			}
		})
	}
	if !bytes.Equal(RequestHash("DELETE", "r", nil), RequestHash("DELETE", "r", []byte("  "))) {
		t.Error("empty and blank bodies hash differently")
	}
}

func TestValidIdempotencyKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"0193a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b", true},
		{"12345678", true},
		{"1234567", false},
		{strings.Repeat("k", 128), true},
		{strings.Repeat("k", 129), false},
		{"has space x", false},
		{"tab\tinside", false},
		{"ünicode-key", false},
	}
	for _, tc := range tests {
		if got := ValidIdempotencyKey(tc.key); got != tc.want {
			t.Errorf("ValidIdempotencyKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
