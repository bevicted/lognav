package logviewer

import (
	"strings"

	"github.com/atotto/clipboard"
	"github.com/bevicted/lognav/internal/filter"
	"github.com/bevicted/lognav/internal/jsonutil"
	"github.com/bevicted/lognav/internal/logging"
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
	"github.com/bevicted/lognav/internal/ui/msgs"
	"github.com/itchyny/gojq"
)

// jqPath builds a gojq path expression from a key-path relative to the log's
// Data map (the jq input root). Keys that are valid jq identifiers are emitted
// dotted (.foo); any other key is emitted in bracket form (.["weird key"]).
// Example: ["data","log","status_code"] -> ".data.log.status_code".
func jqPath(path []string) string {
	var b strings.Builder
	for _, k := range path {
		if isJQIdent(k) {
			b.WriteByte('.')
			b.WriteString(k)
		} else {
			esc := strings.ReplaceAll(k, `\`, `\\`)
			esc = strings.ReplaceAll(esc, `"`, `\"`)
			b.WriteString(`.["`)
			b.WriteString(esc)
			b.WriteString(`"]`)
		}
	}
	return b.String()
}

// isJQIdent reports whether s is a bare jq identifier: [A-Za-z_][A-Za-z0-9_]*.
func isJQIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// dataprimeRef builds a Dataprime field reference from a key-path. The first
// path element selects the sigil (data -> $d, labels -> $l, metadata -> $m);
// the remaining elements are joined with dots. Example:
// ["data","log","status_code"] -> "$d.log.status_code".
func dataprimeRef(path []string) string {
	if len(path) == 0 {
		return ""
	}
	var sigil string
	switch path[0] {
	case "labels":
		sigil = "$l"
	case "metadata":
		sigil = "$m"
	default: // "data" and any unexpected root fall back to $d (user data).
		sigil = "$d"
	}
	if len(path) == 1 {
		return sigil
	}
	return sigil + "." + strings.Join(path[1:], ".")
}

// dataprimeLiteral renders a scalar value as a Dataprime literal. Strings are
// single-quoted with embedded single quotes escaped; numbers/bools/null are
// rendered via canonical JSON. Returns ok=false for objects/arrays (equality is
// meaningless), so the caller omits the query option.
func dataprimeLiteral(v any) (lit string, ok bool) {
	switch t := v.(type) {
	case string:
		esc := strings.ReplaceAll(t, `\`, `\\`)
		esc = strings.ReplaceAll(esc, `'`, `\'`)
		return "'" + esc + "'", true
	case map[string]any, []any, RawLog:
		return "", false
	case nil:
		return "null", true
	default:
		b, err := jsonutil.API.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

// searchValue renders a scalar value as plain search text. Strings are returned
// raw; other scalars via canonical JSON. Returns ok=false for objects/arrays.
func searchValue(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case map[string]any, []any, RawLog:
		return "", false
	case nil:
		return "null", true
	default:
		b, err := jsonutil.API.Marshal(t)
		if err != nil {
			return "", false
		}
		return string(b), true
	}
}

// isScalarValue reports whether v is a scalar (string/number/bool/null) rather
// than a container (object/array). Containers are jq-injectable only.
func isScalarValue(v any) bool {
	switch v.(type) {
	case map[string]any, []any, RawLog:
		return false
	default:
		return true
	}
}

// asMap returns v as a map[string]any, accepting both the plain type and the
// defined RawLog type. A direct v.(map[string]any) assertion fails on a RawLog
// value (Go defined-type rule), so this helper is required for the Data root.
func asMap(v any) (map[string]any, bool) {
	switch t := v.(type) {
	case map[string]any:
		return t, true
	case RawLog:
		return map[string]any(t), true
	default:
		return nil, false
	}
}

// OnInjectSearch sets the search term in state and re-runs search (the ctrl+/
// "search value under cursor" path). No bar involved.
func (m *Model) OnInjectSearch(v string) {
	m.currentMatch = 0
	m.bundle.State.SetSearch(v)
	// Guard mirrors the dialog-Apply path: m.store may be nil until SetStore
	// delivers a store; state is the source of truth and SetStore reapplies.
	if m.store != nil {
		m.store.DoSearch()
	}
}

// OnInjectJQ sets the jq expr in state and re-runs jq (the ctrl+\ "jq field
// under cursor" path). No bar involved.
func (m *Model) OnInjectJQ(v string) {
	// Reset currentMatch: re-running jq changes the match corpus, making any
	// prior jump position meaningless (mirrors the OnInjectSearch reset).
	m.currentMatch = 0
	m.bundle.State.SetJQ(v)
	m.applyJQAndRefresh(strings.TrimSpace(v) == "")
}

// openSearchDialog posts a ShowDialogMsg for the search editor: an input
// prefilled with the current term, Apply/Cancel buttons. Apply sets state.search
// and re-runs search.
func (m *Model) openSearchDialog() {
	in := uiinput.New()
	in.SetValue(m.bundle.State.Search())
	dlg := msgs.ShowDialogMsg{
		Title: "Search",
		Input: in,
		Buttons: []msgs.DialogButton{
			{Label: "Apply", Cmd: func(v string) error {
				m.currentMatch = 0
				m.bundle.State.SetSearch(strings.TrimSpace(v))
				// m.store is nil until SetStore (editing over an empty store is
				// spec-allowed); state is the source of truth and SetStore reapplies
				// the search once a store loads, so just skip the re-run here.
				if m.store != nil {
					m.store.DoSearch()
				}
				return nil
			}},
			{Label: "Cancel"},
		},
	}
	msgs.PostAsync(m.poster, dlg)
}

// openJQDialog posts a ShowDialogMsg for the jq editor: input prefilled with the
// current jq expr, Apply/Cancel. Apply validates via gojq.Parse THEN gojq.Compile;
// an error is returned from the button Cmd (the dialog keeps open + shows the
// error row). A valid expr sets state.jq, re-runs jq, and returns nil to close.
func (m *Model) openJQDialog() {
	in := uiinput.New()
	in.SetValue(m.bundle.State.JQ())
	dlg := msgs.ShowDialogMsg{
		Title: "JQ",
		Input: in,
		Buttons: []msgs.DialogButton{
			{Label: "Apply", Cmd: func(v string) error {
				expr := strings.TrimSpace(v)
				if expr != "" {
					q, err := gojq.Parse(expr)
					if err != nil {
						return err
					}
					if _, err := gojq.Compile(q); err != nil {
						return err
					}
				}
				m.bundle.State.SetJQ(expr)
				m.applyJQAndRefresh(expr == "")
				return nil
			}},
			{Label: "Cancel"},
		},
	}
	msgs.PostAsync(m.poster, dlg)
}

// openFilterMenu posts a ShowFilterMenuMsg carrying the currently-applied rules
// for prefill.
func (m *Model) openFilterMenu() {
	msgs.PostAsync(m.poster, msgs.ShowFilterMenuMsg{Rules: m.bundle.State.Filters()})
}

// onCursorFieldJQ applies the cursor field's jq expression via OnInjectJQ.
// It is the keybind equivalent of the "jq: select field" context-menu item
// (shares jqExprForField) and is a no-op when the cursor is not on a resolvable
// field.
func (m *Model) onCursorFieldJQ() {
	relPath, _, ok := m.resolveCursorField()
	if !ok {
		return
	}
	jq, _, _ := m.jqExprForField(relPath)
	m.OnInjectJQ(jq)
}

// onCursorFieldSearch applies the cursor field's value via OnInjectSearch.
// It is the keybind equivalent of the "search value" context-menu item (shares
// searchValForField) and is a no-op unless the cursor is on a resolvable scalar field.
func (m *Model) onCursorFieldSearch() {
	_, value, ok := m.resolveCursorField()
	if !ok {
		return
	}
	if sv, svOK := m.searchValForField(value); svOK {
		m.OnInjectSearch(sv)
	}
}

// parseJQPathPrefix parses a jq expression that is a pure navigation path
// (a sequence of `.key` and `.["key"]` segments) into its key components. It
// returns (nil, true) for the identity filters "" and ".". It returns ok=false
// for anything that is not a pure path (pipes, functions, array iteration, etc.),
// because such a jq output cannot be mapped back to base-log keys.
func parseJQPathPrefix(jq string) (parts []string, ok bool) {
	s := strings.TrimSpace(jq)
	if s == "" || s == "." {
		return nil, true
	}
	for i := 0; i < len(s); {
		if s[i] != '.' {
			return nil, false
		}
		i++
		if i >= len(s) {
			return nil, false // trailing dot
		}
		if s[i] == '[' {
			j := strings.IndexByte(s[i:], ']')
			if j < 0 {
				return nil, false
			}
			key, kok := unquoteBracketKey(s[i+1 : i+j])
			if !kok {
				return nil, false
			}
			parts = append(parts, key)
			i += j + 1
			continue
		}
		start := i
		for i < len(s) && isJQIdentByte(s[i]) {
			i++
		}
		if i == start {
			return nil, false
		}
		parts = append(parts, s[start:i])
	}
	return parts, true
}

// isJQIdentByte reports whether b can appear in a bare jq identifier segment.
func isJQIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// unquoteBracketKey extracts the object key from the inside of a jq bracket
// index (the text between `[` and `]`), which must be a double-quoted string
// literal. It reverses the escaping jqPath applies (\" -> ", \\ -> \). It
// returns ok=false for non-string indices (e.g. numeric array indices), which
// are not object keys.
func unquoteBracketKey(inner string) (string, bool) {
	inner = strings.TrimSpace(inner)
	if len(inner) < 2 || inner[0] != '"' || inner[len(inner)-1] != '"' {
		return "", false
	}
	body := inner[1 : len(inner)-1]
	body = strings.ReplaceAll(body, `\"`, `"`)
	body = strings.ReplaceAll(body, `\\`, `\`)
	return body, true
}

// valueToClipboard renders a resolved field value for the clipboard. A string is
// returned raw (matching the GetCurrentValue/CopyValue keybind, which copies the
// unquoted string); any other scalar (number/bool/null) or container
// (map/slice/RawLog) is JSON-marshaled via jsonutil.API.Marshal — which handles
// the defined RawLog type, unlike a type assertion (sidestepping the
// GetCurrentValue RawLog-root bug). ok is false only when marshaling fails.
func valueToClipboard(v any) (string, bool) {
	if s, ok := v.(string); ok {
		return s, true
	}
	b, err := jsonutil.API.Marshal(v)
	if err != nil {
		return "", false
	}
	return string(b), true
}

// podIDFromData extracts .data.kubernetes.pod_id (a string) from a log's data
// map. ok is false if any level is missing or not the expected type. It collapses
// the per-level diagnostics (missing vs wrong-type at each of .data/.kubernetes/
// .pod_id) into a single ok=false; callers wanting a diagnostic log a single
// generic message. Shared by hasPodContext (the "dataprime: pod context" menu item
// visibility predicate) and viewInContext (which resolves the same field).
func podIDFromData(d RawLog) (string, bool) {
	data, ok := d["data"].(map[string]any)
	if !ok {
		return "", false
	}
	kube, ok := data["kubernetes"].(map[string]any)
	if !ok {
		return "", false
	}
	podID, ok := kube["pod_id"].(string)
	if !ok {
		return "", false
	}
	return podID, true
}

// hasPodContext reports whether the log under the cursor has a Kubernetes pod id
// (.data.kubernetes.pod_id). It is the visibility predicate for the "query pod
// context" menu item — the item is hidden when viewInContext would no-op.
func (m *Model) hasPodContext() bool {
	// The nil-poster guard lives upstream in showContextMenu, this method's only
	// caller; hasPodContext only needs a non-empty store.
	if m.store == nil || m.store.GetLogCount() < 1 {
		return false
	}
	d, _, u := m.store.logs[m.cursor.log].GetData()
	defer u()
	_, ok := podIDFromData(d)
	return ok
}

// contextMenuFieldItems builds the field-related context-menu items when the
// cursor sits on a resolvable field in an expanded log. It returns nil when no
// field is resolved.
//
// Ordering contract (relied on by postContextMenu's splice): "copy value" is
// first, followed by search, jq, filter, and Dataprime actions. Each action is
// subject to its own applicability conditions.
// jqExprForField builds the jq expression that targets the resolved field path
// under the cursor, mirroring the active jq: when the applied jq is a pure
// navigation path it extends that path and returns the fully-qualified basePath
// (jqIsPath true); otherwise it appends the field's path to the applied jq
// (basePath nil, jqIsPath false). It is the single source of the jq field
// expression for both the context menu and the jq-field keybind.
func (m *Model) jqExprForField(relPath []string) (jq string, basePath []string, jqIsPath bool) {
	prefix, isPath := parseJQPathPrefix(m.store.appliedJQ)
	if isPath {
		basePath = append(append([]string{}, prefix...), relPath...)
		return jqPath(basePath), basePath, true
	}
	return m.store.appliedJQ + jqPath(relPath), nil, false
}

// searchValForField returns the search string for a resolved field value,
// mirroring the "search value" context-menu item. ok is false unless the value
// is a scalar with a usable search representation. It is the single source of
// the "search value" gate for both the context menu and the search-value
// keybind.
func (m *Model) searchValForField(value any) (sv string, ok bool) {
	if !isScalarValue(value) {
		return "", false
	}
	return searchValue(value)
}

// appendFilterRule appends rule {t, value} to the applied filter set and applies
// it immediately: it reads state.Filters(), appends, writes state.SetFilters, and
// posts FilterAppliedMsg (the recompute). It runs on the loop (a context-menu
// Action), hence msgs.PostAsync.
func (m *Model) appendFilterRule(t filter.Type, value string) {
	rules := append(m.bundle.State.Filters(), filter.Rule{Type: t, Value: value})
	m.bundle.State.SetFilters(rules)
	msgs.PostAsync(m.poster, msgs.FilterAppliedMsg{})
}

func (m *Model) contextMenuFieldItems() []msgs.ContextMenuItem {
	relPath, value, ok := m.resolveCursorField()
	if !ok {
		return nil
	}

	jq, basePath, jqIsPath := m.jqExprForField(relPath)

	val := value
	var items []msgs.ContextMenuItem

	items = append(items, msgs.ContextMenuItem{Label: "copy value", Action: func() {
		if s, clipOK := valueToClipboard(val); clipOK {
			if err := clipboard.WriteAll(s); err != nil {
				m.logger.Error("clipboard copy failed", logging.KeyError, err)
			}
		}
	}})

	if sv, svOK := m.searchValForField(value); svOK {
		items = append(items, msgs.ContextMenuItem{Label: "search value", Action: func() {
			m.OnInjectSearch(sv)
		}})
	}

	jqExpr := jq
	items = append(items, msgs.ContextMenuItem{Label: "jq: select field", Action: func() {
		m.OnInjectJQ(jqExpr)
	}})

	// Term-rule items use the search-text form of scalar values.
	if sv, svOK := m.searchValForField(value); svOK {
		items = append(items, msgs.ContextMenuItem{Label: "filter: include value", Action: func() {
			m.appendFilterRule(filter.Include, sv)
		}})
		items = append(items, msgs.ContextMenuItem{Label: "filter: exclude value", Action: func() {
			m.appendFilterRule(filter.Exclude, sv)
		}})
	}

	// Field filter rules are evaluated against raw log data, so the rule value
	// must be the field's absolute raw path. Under a non-path jq the displayed
	// relative path cannot be mapped back to raw, so this item is omitted.
	if jqIsPath {
		fieldPath := jqPath(basePath)
		items = append(items, msgs.ContextMenuItem{Label: "filter: has field", Action: func() {
			m.appendFilterRule(filter.HasField, fieldPath)
		}})
	}

	if jqIsPath && isScalarValue(value) {
		if lit, litOK := dataprimeLiteral(value); litOK {
			q := "| filter " + dataprimeRef(basePath) + " == " + lit
			items = append(items, msgs.ContextMenuItem{Label: "dataprime: filter field == value", Action: func() {
				msgs.PostAsync(m.poster, msgs.InjectQueryMsg{Snippet: q})
			}})
		}
	}

	return items
}

// showContextMenu opens the context menu via the keybind (`c`): it anchors below
// the selected field, at the column of the line's first non-blank character (the
// `^` target). It is a no-op when there is no log under the cursor or no poster.
func (m *Model) showContextMenu() {
	ax, ay := m.contextMenuAnchor()
	m.postContextMenu(ax, ay)
}

// showContextMenuAt opens the context menu via right-click: it anchors at the
// pointer column, one row BELOW the pointer (y+1, the same below-the-reference
// convention the `c` path uses with cursor.y+1), so the menu opens under the
// mouse without covering the clicked line — the conventional right-click
// placement.
func (m *Model) showContextMenuAt(x, y int) {
	m.postContextMenu(x, y+1)
}

// postContextMenu builds the context-menu items applicable to the current log
// and cursor and posts a ShowContextMenuMsg anchored at (ax, ay) off-loop.
// Inapplicable items are omitted: field actions appear only when the cursor sits
// on a resolvable field; value actions additionally require a scalar, raw-field
// actions require a path-navigable active jq, and pod context requires a
// Kubernetes pod id. It is a no-op when there is no log under the cursor or no
// poster.
func (m *Model) postContextMenu(ax, ay int) {
	if m.store == nil || m.store.GetLogCount() < 1 || m.poster == nil {
		return
	}
	logIdx := m.cursor.log

	fieldItems := m.contextMenuFieldItems()

	var items []msgs.ContextMenuItem

	// Keep the two copy actions together, then append the remaining field groups.
	var afterCopyLog []msgs.ContextMenuItem
	if len(fieldItems) > 0 {
		items = append(items, fieldItems[0])
		afterCopyLog = fieldItems[1:]
	}

	// copy log — always.
	items = append(items, msgs.ContextMenuItem{Label: "copy log", Action: func() {
		l := m.store.logs[logIdx]
		b, err := l.marshal(m.store.state.expanded[logIdx])
		if err != nil {
			m.logger.Error("clipboard marshal failed", logging.KeyError, err)
			return
		}
		if err := clipboard.WriteAll(string(b)); err != nil {
			m.logger.Error("clipboard copy failed", logging.KeyError, err)
		}
	}})

	items = append(items, afterCopyLog...)

	// Keep the Dataprime actions contiguous.
	if m.hasPodContext() {
		items = append(items, msgs.ContextMenuItem{Label: "dataprime: pod context", Action: m.viewInContext})
	}

	markLabel := "mark log"
	if m.IsMarked(logIdx) {
		markLabel = "unmark log"
	}
	items = append(items, msgs.ContextMenuItem{Label: markLabel, Action: func() {
		m.store.state.marked.Set(logIdx, !m.IsMarked(logIdx))
	}})

	msgs.PostAsync(m.poster, msgs.ShowContextMenuMsg{Items: items, Anchored: true, AnchorX: ax, AnchorY: ay})
}

// contextMenuAnchor returns the absolute screen cell the context menu's top-left
// corner should sit at: the column of the current line's first non-blank
// character (the `^` target, honoring the current horizontal scroll) and the row
// just below the selected line. Callers guard that a log is under the cursor.
func (m *Model) contextMenuAnchor() (x, y int) {
	_, logsR, _ := m.subRects()
	l := m.GetCurrentLine()
	firstNonBlank := len(l) - len(strings.TrimLeft(l, " "))
	x = logsR.X + max(0, firstNonBlank-m.xOffset)
	y = logsR.Y + m.cursor.y + 1
	return x, y
}

// resolveCursorField resolves the field under the cursor in an expanded log. It
// returns the key-path (relative to the Data map), the raw value at that path,
// and ok=false when there is no log, the log is not expanded, no resolvable key
// on the cursor line, or the path does not navigate to a value.
func (m *Model) resolveCursorField() (path []string, value any, ok bool) {
	if m.store == nil || m.store.GetLogCount() < 1 {
		return nil, nil, false
	}
	if !m.store.state.expanded[m.cursor.log] {
		return nil, nil, false
	}
	lines := m.getRenderCacheEntry(m.cursor.log)
	path = extractKeyPath(lines, m.cursor.logLine)
	if len(path) == 0 {
		return nil, nil, false
	}
	data, modified, unlock := m.store.logs[m.cursor.log].GetData()
	defer unlock()
	var current any = map[string]any(data)
	if modified != nil {
		current = modified
	}
	for _, key := range path {
		d, isMap := asMap(current)
		if !isMap {
			return nil, nil, false
		}
		current, ok = d[key]
		if !ok {
			return nil, nil, false
		}
	}
	return path, current, true
}
