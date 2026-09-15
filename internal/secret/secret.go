// Package secret provides a self-redacting string type for credential
// values. Use String for any field that holds a secret token; both
// LogValue and Stringer always return [REDACTED] so the value cannot leak
// through slog, fmt, or println paths. JSON marshaling under
// encoding/json emits the raw string (kind-based, ignores Stringer) so
// disk-persisted secrets keep their format; tests cover sonic round-trip
// parity.
package secret

import "log/slog"

// String is a self-redacting string type for credential values.
type String string

// LogValue returns the redacted form for slog handlers. Always [REDACTED]
// — empty values are treated as secret to avoid distinguishing between
// "no secret" and "cleared secret" via log shape.
func (s String) LogValue() slog.Value {
	return slog.StringValue("[REDACTED]")
}

// String returns [REDACTED] for fmt formatters that respect Stringer.
// Note: fmt verbs %s and %v call Stringer; %q (and json.Marshal) call
// neither and emit the raw value. Use string(s) for explicit conversion
// when the raw value is required.
func (s String) String() string {
	return "[REDACTED]"
}
