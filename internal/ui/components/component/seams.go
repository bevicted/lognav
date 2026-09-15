package component

import (
	"fmt"
	"log/slog"
	"reflect"
	"strings"

	"github.com/bevicted/lognav/internal/logging"
)

// seam pairs an optional input-seam interface with the name the inventory
// reports it under.
type seam struct {
	name string
	typ  reflect.Type
}

// ifaceType returns the reflect.Type of the interface T.
func ifaceType[T any]() reflect.Type { return reflect.TypeFor[T]() }

// inputSeams is the catalog the startup inventory walks: every optional input
// seam a component may opt into, in dispatch-precedence-ish order. It is the
// SECOND safety net, not the first — the first is the per-component
// `var _ component.X = (*Model)(nil)` assertion block, which fails the build.
// This one only makes a missing opt-in visible in the log.
//
// A seam added to this package must be added here too, otherwise the inventory
// silently stops covering it.
var inputSeams = []seam{
	{"KeyTarget", ifaceType[KeyTarget]()},
	{"MouseTarget", ifaceType[MouseTarget]()},
	{"SecondaryMouseTarget", ifaceType[SecondaryMouseTarget]()},
	{"DoubleClickTarget", ifaceType[DoubleClickTarget]()},
	{"MousePasteTarget", ifaceType[MousePasteTarget]()},
	{"ScrollTarget", ifaceType[ScrollTarget]()},
	{"MouseHoverTarget", ifaceType[MouseHoverTarget]()},
	{"PasteTarget", ifaceType[PasteTarget]()},
	{"FocusReleaser", ifaceType[FocusReleaser]()},
	{"ContextMenuOpener", ifaceType[ContextMenuOpener]()},
}

// Seams reports which optional input seams target satisfies and which it does
// not, both in catalog order. A nil target satisfies nothing. Seams answers the
// question the dispatch-site type assertions ask, but away from the hot path:
// the assertions run per event, this runs once.
func Seams(target any) (implemented, missing []string) {
	t := reflect.TypeOf(target)
	for _, s := range inputSeams {
		if t != nil && t.Implements(s.typ) {
			implemented = append(implemented, s.name)
			continue
		}
		missing = append(missing, s.name)
	}
	return implemented, missing
}

// LogSeams logs ONE debug line recording which optional input seams target opts
// into and which it does not. Call it once per registered component at wiring
// time.
//
// Deliberately NOT at the dispatch sites: hover and scroll fire per mouse event,
// so logging there would spam the log continuously for every component that
// legitimately does not implement the seam. Debug level matches the root's
// "unhandled msg" log, so the two are visible under the same logging flag.
//
// name is the human label for the component (a tab title); it falls back to the
// concrete type when empty.
func LogSeams(logger *slog.Logger, name string, target any) {
	if logger == nil {
		logger = slog.Default()
	}
	typeName := fmt.Sprintf("%T", target)
	if name == "" {
		name = typeName
	}
	implemented, missing := Seams(target)
	logger.Debug("input seam inventory",
		"target", name,
		logging.KeyType, typeName,
		"implements", strings.Join(implemented, ","),
		"missing", strings.Join(missing, ","),
	)
}
