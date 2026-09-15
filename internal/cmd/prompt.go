package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

// isTTY reports whether stdin is an interactive terminal. Package var so tests
// can force interactive / non-interactive behavior.
var isTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// promptIn is the reader confirm prompts read from. Package var for tests.
var promptIn io.Reader = os.Stdin

// confirmYesNo prints question to out and reads one line from promptIn. The
// default (empty line) is yes; "y"/"yes" (case-insensitive) is yes, anything
// else is no.
func confirmYesNo(out io.Writer, question string) (bool, error) {
	fmt.Fprintf(out, "%s ", question)
	line, err := bufio.NewReader(promptIn).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
