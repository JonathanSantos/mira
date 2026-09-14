package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOldFlagNamesStillWork: as flags têm os nomes dos parâmetros MCP, e os
// nomes antigos continuam aceitos pelo parser.
func TestOldFlagNamesStillWork(t *testing.T) {
	tests := []struct {
		command        []string
		old, canonical string
	}{
		{[]string{"resolve"}, "no-tests", "exclude-tests"},
		{[]string{"resolve"}, "skeleton", "include-skeleton"},
		{[]string{"resolve"}, "with-refs", "include-refs"},
		{[]string{"refs"}, "no-tests", "exclude-tests"},
		{[]string{"files"}, "no-tests", "exclude-tests"},
		{[]string{"search"}, "no-tests", "exclude-tests"},
		{[]string{"edit", "replace-in"}, "old", "old-text"},
		{[]string{"edit", "replace-in"}, "new", "new-text"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.command, " ")+" --"+tt.old, func(t *testing.T) {
			cmd, _, err := New().Find(tt.command)
			require.NoError(t, err)
			require.NoError(t, cmd.Flags().Parse([]string{"--" + tt.old + "=true"}))
			flag := cmd.Flags().Lookup(tt.canonical)
			require.NotNil(t, flag, "--%s", tt.canonical)
			assert.Equal(t, tt.canonical, flag.Name)
			assert.True(t, flag.Changed, "--%s sets --%s", tt.old, tt.canonical)
		})
	}
}
