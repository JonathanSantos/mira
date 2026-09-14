package resolve

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemember(t *testing.T) {
	tests := []struct {
		name      string
		loads     []error // resultado de cada load, na ordem
		wantCalls int
	}{
		{"a stored answer is not loaded again", []error{nil, nil}, 1},
		{"an error is not stored", []error{errors.New("busy"), nil}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]int{}
			calls := 0
			load := func() (int, error) {
				err := tt.loads[calls]
				calls++
				return 42, err
			}
			for range tt.loads {
				v, err := remember(m, "k", load)
				if err == nil {
					assert.Equal(t, 42, v)
				}
			}
			assert.Equal(t, tt.wantCalls, calls)
		})
	}
}
