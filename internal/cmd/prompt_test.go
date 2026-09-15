package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConfirmYesNo(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true}, {"yes\n", true}, {"\n", true}, // default yes
		{"n\n", false}, {"no\n", false}, {"x\n", false},
	}
	for _, tc := range cases {
		orig := promptIn
		promptIn = strings.NewReader(tc.in)
		var out bytes.Buffer
		got, err := confirmYesNo(&out, "Adopt? [y]/n")
		promptIn = orig
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "input %q", tc.in)
	}
}
