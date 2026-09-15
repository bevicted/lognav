package keys

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

const ConfigKey = "keybinds"

type Action func()

type Binding struct {
	Keys    []string
	Context string
	Action  Action
	// Category buckets the binding in the sectioned help menu and selects its
	// colored category column. The zero value (CatThisView) means the binding is
	// a context action of the active tab, so component feature bindings need no
	// explicit category.
	Category Category
	// Label is the lowercase text shown by a curated key hint. It does not
	// affect dispatch or the sectioned help overlay.
	Label string
	// Priority opts a binding into the key hint line. Zero is hidden; positive
	// priorities are ordered ascending, then alphabetically by Label.
	Priority int
}

// HintBindings returns the bindings opted into the hint line, in their display
// order. It copies before sorting so callers' binding and help-overlay order is
// unchanged.
func HintBindings(bindings []Binding) []Binding {
	hints := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Priority > 0 {
			hints = append(hints, binding)
		}
	}
	slices.SortStableFunc(hints, func(a, b Binding) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return strings.Compare(strings.ToLower(a.Label), strings.ToLower(b.Label))
	})
	return hints
}

// HintPill returns the two pill bands for a binding. A bare lowercase first
// key may stand in for a matching first label character; all other keys retain
// their complete canonical gesture so Shift and modifier keys are unambiguous.
func (b Binding) HintPill() (chip, line string) {
	if len(b.Keys) == 0 {
		return "", b.Label
	}
	key := canonicalizeBindKey(b.Keys[0])
	if isBareLowercase(key) && strings.HasPrefix(b.Label, key) {
		return key, strings.TrimPrefix(b.Label, key)
	}
	return key, b.Label
}

func isBareLowercase(key string) bool {
	r, size := utf8.DecodeRuneInString(key)
	return size == len(key) && unicode.IsLower(r)
}

func (b Binding) WithPrefix(p string) Binding {
	b.Context = fmt.Sprintf("%s: %s", p, b.Context)
	return b
}

func (b Binding) WithAction(a Action) Binding {
	b.Action = a
	return b
}

// WithHint returns a binding copy opted into the key hint line. It leaves the
// original binding untouched so shared handler bindings can be curated per
// consumer without leaking metadata into another context.
func (b Binding) WithHint(label string, priority int) Binding {
	b.Label = label
	b.Priority = priority
	return b
}

func (b Binding) GetKeys() []string {
	return b.Keys
}

type Handler struct {
	actions  map[string]int
	keybinds []Binding
}

func New() *Handler {
	return &Handler{
		actions: make(map[string]int),
	}
}

func (kh *Handler) Bind(b ...Binding) *Handler {
	idxOffset := len(kh.keybinds)
	for idx := range b {
		for _, key := range b[idx].Keys {
			kh.actions[canonicalizeBindKey(key)] = idxOffset + idx
		}
	}
	kh.keybinds = append(kh.keybinds, b...)
	return kh
}

// normalize returns the canonical lognav keystroke string for a uv key event,
// collapsing the legacy and enhanced terminal encodings of the same physical
// key to one form. A bare shifted printable on a legacy terminal (Text "Y",
// Mod 0) and the enhanced form (Code 'y', Mod ModShift) both yield "shift+y".
//
// uv's Keystroke() emits "shift+" only when ModShift is set (key.go:420), so a
// legacy uppercase rune would otherwise resolve to "Y" and miss a canonical
// "shift+y" binding; this branch reproduces, at lookup time, the same
// canonicalization uv's keyMatchString applies at match time. We keep it here
// (not via MatchString) because Handler dispatch is map-based (O(1)).
func normalize(k uv.KeyPressEvent) string {
	if k.Mod&uv.ModShift == 0 && len(k.Text) > 0 {
		r, sz := utf8.DecodeRuneInString(k.Text)
		if sz == len(k.Text) && unicode.IsUpper(r) {
			return "shift+" + string(unicode.ToLower(r))
		}
	}
	return k.Keystroke()
}

// canonicalizeBindKey maps a config/default key string to the canonical form
// normalize() produces. It preserves compatibility with legacy key names and
// treats a bare uppercase letter as shift+<letter>.
func canonicalizeBindKey(s string) string {
	if s == "del" {
		return "delete"
	}
	r, size := utf8.DecodeRuneInString(s)
	if size == len(s) && unicode.IsUpper(r) {
		return "shift+" + string(unicode.ToLower(r))
	}
	return s
}

// GetAction resolves a key event to its bound action via the normalize()
// chokepoint.
func (kh *Handler) GetAction(ev uv.KeyPressEvent) Action {
	if idx, ok := kh.actions[normalize(ev)]; ok {
		return kh.keybinds[idx].Action
	}
	return nil
}

// Run resolves ev to its bound action, invokes it, and reports whether an
// action ran. It is the single dispatch idiom for every key handler group: a
// miss (unbound key, or a display-only binding with no Action) reports false so
// the caller can fall through to the next group.
func (kh *Handler) Run(ev uv.KeyPressEvent) bool {
	a := kh.GetAction(ev)
	if a == nil {
		return false
	}
	a()
	return true
}

func (kh *Handler) GetKeybinds() []Binding {
	return kh.keybinds
}
