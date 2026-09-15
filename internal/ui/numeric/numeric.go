// Package numeric provides small numeric helpers shared across UI components.
package numeric

// Clamp returns v constrained to [low, high].
func Clamp(v, low, high int) int {
	return max(min(v, high), low)
}
