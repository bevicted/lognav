package msgs

// NotifyMsg is posted when a fetch completes for all instances and the
// Core.NotifyOnFetchDone config is enabled. The runtime handles it by emitting a
// terminal notification (currently the audible bell, BEL/0x07). The indirection
// keeps the notification mechanism in the runtime — which owns the screen — and
// lets the trigger site stay mechanism-agnostic so the notification kind can grow
// (e.g. an OSC desktop notification) without touching the instancepicker.
type NotifyMsg struct{}
