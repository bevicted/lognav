package cmd

import "bytes"

const loginPasscodeMaxRunes = 10

var (
	loginPasteStart = []byte("\x1b[200~")
	loginPasteEnd   = []byte("\x1b[201~")
)

// normalizeLoginPasscode matches the TUI passcode dialog: terminal paste
// framing is discarded and uiinput's SetMaxLen(10) retains the first 10 runes.
func normalizeLoginPasscode(input []byte) string {
	value := []rune(string(unframeLoginPasscode(input)))
	if len(value) > loginPasscodeMaxRunes {
		value = value[:loginPasscodeMaxRunes]
	}
	return string(value)
}

// unframeLoginPasscode removes only a complete terminal bracketed-paste frame.
func unframeLoginPasscode(input []byte) []byte {
	if hasCompleteLoginPasteFrame(input) {
		return input[len(loginPasteStart) : len(input)-len(loginPasteEnd)]
	}
	return input
}

func hasCompleteLoginPasteFrame(input []byte) bool {
	return bytes.HasPrefix(input, loginPasteStart) && bytes.HasSuffix(input, loginPasteEnd)
}
