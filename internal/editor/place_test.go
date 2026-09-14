package editor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPlace(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		word   string
		sites  []site
		cols   []int
		reason string
	}{
		{"column on the name", "  return plus(a, b);", "plus", []site{{col: 10}}, []int{10}, ""},
		{"receiver right before the name", "\tPrice pricing.Money", "Money", []site{{col: 8, receiver: "pricing"}}, []int{16}, ""},
		{"two qualified references on one line", "def pair(a: money.Money, b: money.Money):", "Money",
			[]site{{col: 13, receiver: "money"}, {col: 29, receiver: "money"}}, []int{19, 35}, ""},
		{"nodes starting earlier pair with occurrences in order", "x = mod.Money(mod.Money())", "Money",
			[]site{{col: 5}, {col: 15}}, []int{9, 19}, ""},
		{"a string with the same word is not an occurrence", `def total(label="total"):`, "total",
			[]site{{decl: true}}, []int{5}, ""},
		{"an import path is not an occurrence", "import { createFormControl } from '../logic/createFormControl';", "createFormControl",
			[]site{{col: 1}}, []int{10}, ""},
		{"an exact declaration leaves the string alone", `def total(label="total"):`, "total",
			[]site{{col: 5, decl: true}}, []int{5}, ""},
		{"never guesses between identifiers", "plus(plus, plus)", "plus",
			[]site{{decl: true}}, nil, "3 occurrences of plus for 1 reference(s), position unknown"},
		{"exact positions change even when another is unknown", "plus(plus, plus)", "plus",
			[]site{{col: 1}, {decl: true}}, []int{1}, "2 occurrences of plus for 1 reference(s), position unknown"},
		{"escaped quotes stay inside the string", `x = "a \"plus\" b" + plus`, "plus",
			[]site{{col: 1}}, []int{22}, ""},
		{"name gone from the line", "return 0", "plus", []site{{col: 3}}, nil, "name not found on the line (index out of date?)"},
		{"whole words only", "totals = total + subtotal", "total", []site{{col: 10}}, []int{10}, ""},
		{"utf-8 identifiers are not split", "café = caf", "caf", []site{{col: 9}}, []int{9}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cols, reason := place(tc.line, tc.word, tc.sites)
			assert.Equal(t, tc.cols, cols)
			assert.Equal(t, tc.reason, reason)
		})
	}
}

func TestIsIdentifier(t *testing.T) {
	tests := map[string]bool{"sum": true, "_x1": true, "$el": true, "café": true, "1st": false, "a-b": false, "a b": false, "": false}
	for in, want := range tests {
		assert.Equal(t, want, isIdentifier(in), in)
	}
}
