package render_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/JonathanSantos/mira/internal/editor"
	"github.com/JonathanSantos/mira/internal/render"
)

func TestEditRename(t *testing.T) {
	res := editor.Result{Action: editor.Rename, File: "a.ts", Symbol: "plus", NewName: "sum", Checked: 14,
		Files:   []editor.FileChange{{File: "a.ts", Changes: 1}, {File: "b.ts", Changes: 14}},
		Updated: []editor.Site{{File: "a.ts", Line: 1}},
		Skipped: []editor.Site{{File: "c.ts", Line: 9, Reason: "unresolved: may be this symbol, left untouched", Text: "x.plus(1)"}},
		Notes:   []string{"1 uses of plus resolve to other symbols or libraries and were left alone"},
	}
	for i := 1; i <= 14; i++ {
		res.Updated = append(res.Updated, editor.Site{File: "b.ts", Line: i})
	}
	assert.Equal(t, "rename plus -> sum: 15 lines in 2 files; index refreshed\n"+
		"  a.ts: 1\n"+
		"  b.ts: 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, … (+2 more)\n"+
		"  verified: 14 of 14 references still resolve\n"+
		"  skipped (1):\n"+
		"    c.ts:9 unresolved: may be this symbol, left untouched: x.plus(1)\n"+
		"  note: 1 uses of plus resolve to other symbols or libraries and were left alone\n", render.Edit(res))
}

func TestEditDeleteAndPreview(t *testing.T) {
	forced := editor.Result{Action: editor.Delete, File: "a.ts", Symbol: "Cart", StartLine: 3, EndLine: 9,
		Dangling: []editor.Site{{File: "b.ts", Line: 4, Reason: "still points to the deleted symbol"}}}
	assert.Equal(t, "delete Cart: a.ts:3-9 (7 lines removed); index refreshed\n"+
		"  dangling (1):\n    b.ts:4 still points to the deleted symbol\n", render.Edit(forced))

	preview := editor.Result{Action: editor.Replace, File: "a.ts", Symbol: "total", StartLine: 2, EndLine: 4, Lines: 3, Preview: true,
		Diff: "a.ts\n  @@ 2-3 -> 3 lines\n"}
	assert.Equal(t, "replace total: a.ts:2-4 (3 lines) (preview: nothing written)\na.ts\n  @@ 2-3 -> 3 lines\n", render.Edit(preview))
}
