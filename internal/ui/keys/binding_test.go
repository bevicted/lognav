package keys

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/assert"
)

const (
	keyShiftY = "shift+y"
	keyCtrlC  = "ctrl+c"
	keyPgDown = "pgdown"
)

func TestNormalize_CanonicalForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ev   uv.KeyPressEvent
		want string
	}{
		{"legacy shift-x", uv.KeyPressEvent{Code: 'X', Text: "X", Mod: 0}, "shift+x"},
		{"legacy shift-l", uv.KeyPressEvent{Code: 'L', Text: "L", Mod: 0}, "shift+l"},
		{"legacy shift-h", uv.KeyPressEvent{Code: 'H', Text: "H", Mod: 0}, "shift+h"},
		{"legacy shift-g", uv.KeyPressEvent{Code: 'G', Text: "G", Mod: 0}, "shift+g"},
		{"legacy shift-n", uv.KeyPressEvent{Code: 'N', Text: "N", Mod: 0}, "shift+n"},
		{"legacy shift-c", uv.KeyPressEvent{Code: 'C', Text: "C", Mod: 0}, "shift+c"},
		{"legacy shift-u", uv.KeyPressEvent{Code: 'U', Text: "U", Mod: 0}, "shift+u"},
		{"enhanced shift-y", uv.KeyPressEvent{Code: 'y', Text: "Y", Mod: uv.ModShift}, keyShiftY},
		{"lowercase a", uv.KeyPressEvent{Code: 'a', Text: "a", Mod: 0}, "a"},
		{"ctrl+c", uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl}, keyCtrlC},
		{"pgdown", uv.KeyPressEvent{Code: uv.KeyPgDown}, keyPgDown},
		{"enter", uv.KeyPressEvent{Code: uv.KeyEnter}, "enter"},
		{"delete", uv.KeyPressEvent{Code: uv.KeyDelete}, "delete"},
		{"f1", uv.KeyPressEvent{Code: uv.KeyF1}, "f1"},
		{"shift+right", uv.KeyPressEvent{Code: uv.KeyRight, Mod: uv.ModShift}, "shift+right"},
		{"space", uv.KeyPressEvent{Code: uv.KeySpace, Text: " "}, "space"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, normalize(tt.ev))
		})
	}
}

func TestCanonicalizeBindKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"bare uppercase Y", "Y", keyShiftY},
		{"bare uppercase X", "X", "shift+x"},
		{"already canonical shift+y", keyShiftY, keyShiftY},
		{"named key pgdown", keyPgDown, keyPgDown},
		{"legacy named key del", "del", "delete"},
		{"canonical named key delete", "delete", "delete"},
		{"ctrl combo", keyCtrlC, keyCtrlC},
		{"lowercase a", "a", "a"},
		{"symbol passthrough", "<", "<"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, canonicalizeBindKey(tt.input))
		})
	}
}

func TestHandler_GetAction_DeleteAlias(t *testing.T) {
	t.Parallel()
	kh := New().Bind(Binding{
		Keys:   []string{"del"},
		Action: func() {},
	})

	assert.NotNil(t, kh.GetAction(uv.KeyPressEvent{Code: uv.KeyDelete}))
}

func TestHandler_GetAction_DualAcceptance(t *testing.T) {
	t.Parallel()
	legacy := uv.KeyPressEvent{Code: 'Y', Text: "Y", Mod: 0}
	enhanced := uv.KeyPressEvent{Code: 'y', Text: "Y", Mod: uv.ModShift}

	cases := []struct {
		name    string
		bindKey string
	}{
		{"bare uppercase registers canonical", "Y"},
		{"explicit shift form registers canonical", keyShiftY},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kh := New().Bind(Binding{
				Keys:   []string{tc.bindKey},
				Action: func() {},
			})
			assert.NotNil(t, kh.GetAction(legacy), "legacy Shift+Y must resolve")
			assert.NotNil(t, kh.GetAction(enhanced), "enhanced Shift+Y must resolve")
		})
	}
}

func TestHintBindings(t *testing.T) {
	t.Parallel()
	bindings := []Binding{
		{Label: "hidden", Priority: 0},
		{Label: "zulu", Priority: 2},
		{Label: "alpha", Priority: 2},
		{Label: "help", Priority: 1},
	}

	hints := HintBindings(bindings)
	assert.Equal(t, []string{"help", "alpha", "zulu"}, []string{
		hints[0].Label,
		hints[1].Label,
		hints[2].Label,
	})
	assert.Equal(t, "zulu", bindings[1].Label, "hint sorting must not reorder help bindings")
}

func TestBinding_WithHintReturnsCopy(t *testing.T) {
	t.Parallel()
	original := Binding{Label: "original", Priority: 1}
	decorated := original.WithHint("save", 5)

	assert.Equal(t, "original", original.Label)
	assert.Equal(t, 1, original.Priority)
	assert.Equal(t, "save", decorated.Label)
	assert.Equal(t, 5, decorated.Priority)
}

func TestBinding_HintPill(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		binding  Binding
		wantChip string
		wantLine string
	}{
		{"matching lowercase key", Binding{Keys: []string{"f"}, Label: "fetch"}, "f", "etch"},
		{"uppercase key is Shift", Binding{Keys: []string{"F"}, Label: "fetch"}, "shift+f", "fetch"},
		{"modified key", Binding{Keys: []string{"ctrl+s"}, Label: "save"}, "ctrl+s", "save"},
		{"named key", Binding{Keys: []string{"space"}, Label: "select"}, "space", "select"},
		{"mismatched lowercase key", Binding{Keys: []string{"f"}, Label: "save"}, "f", "save"},
		{"first configured key is canonical", Binding{Keys: []string{"ctrl+s", "s"}, Label: "save"}, "ctrl+s", "save"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			chip, line := tt.binding.HintPill()
			assert.Equal(t, tt.wantChip, chip)
			assert.Equal(t, tt.wantLine, line)
		})
	}
}
