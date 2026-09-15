package msgs

// QuitMsg is posted to request a clean exit; the runtime's dispatch loop sets
// the quit flag on receipt (R5a).
type QuitMsg struct{}
