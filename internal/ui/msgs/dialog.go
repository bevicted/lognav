package msgs

import (
	"github.com/bevicted/lognav/internal/ui/components/uiinput"
)

// DialogButton represents a button in a dialog. Cmd is called with the input
// value when the button is pressed; nil means the button only dismisses the
// dialog. A non-nil error return keeps the dialog open and surfaces the error in
// a red row below the input (used by the jq dialog for parse/compile errors);
// returning nil closes the dialog as usual.
type DialogButton struct {
	Label string
	Cmd   func(string) error
}

// ShowDialogMsg triggers a dialog overlay. Any component can post this message
// to the runtime to produce the overlay.
type ShowDialogMsg struct {
	Title   string
	Message string
	// LinkURL, when non-empty, marks the Message line whose text equals it as a
	// hyperlink: the dialog renders that line in the configured UrlFg color,
	// underlined, and with a real OSC8 link so it is click-to-open. The Message
	// itself stays plain text (no escape sequences).
	LinkURL string
	Buttons []DialogButton
	Input   *uiinput.Input // nil = no input; caller configures via uiinput.New()+SetValue/SetMasked/SetMaxLen
	// PillChip, when non-empty, renders the input as a pill: a bright chip label
	// (e.g. "S" / "J") immediately left of the input, with the PillLine band
	// behind the input cells. Empty = a plain input (the prior behavior).
	PillChip string
	OnCancel func() // optional: fires when dialog is dismissed via Esc/Quit (not via a confirmed button)
}
