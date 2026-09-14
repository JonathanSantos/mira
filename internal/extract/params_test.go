package extract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParamBounds(t *testing.T) {
	tests := []struct {
		sig      string
		min, max int
		ok       bool
	}{
		{"public Pet getPet(Integer id)", 1, 1, true},
		{"public Pet getPet(String name, boolean ignoreNew)", 2, 2, true},
		{"public String processFindForm(@RequestParam(defaultValue = \"1\") int page, Owner owner, BindingResult result, Model model)", 4, 4, true},
		{"static <T> List<T> of(Map<String, List<Integer>> m, T... items)", 1, -1, true},
		{"public Cache()", 0, 0, true},
		{"export function applyCoupon(total: number, coupon?: string): number", 1, 2, true},
		{"const register: UseFormRegister<TFieldValues, TTransformedValues> = (name, options = {}) =>", 1, 2, true},
		{"export function f(a: (x: number) => void, ...rest: string[]): void", 1, -1, true},
		{"total(order: Order): number", 1, 1, true},
		{"const cb = x =>", 0, 0, false},
		{"private final int percent", 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.sig, func(t *testing.T) {
			minArgs, maxArgs, ok := ParamBounds(tt.sig)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.min, minArgs, "min")
				assert.Equal(t, tt.max, maxArgs, "max")
			}
		})
	}
}
