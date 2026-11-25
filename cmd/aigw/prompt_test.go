// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	keyUp    = "\x1b[A"
	keyRight = "\x1b[C"
)

var testItems = []selectItem{
	{label: "one", detail: "first"},
	{label: "two", detail: "second"},
	{label: "three", detail: "third"},
}

// stutterReader returns (0, nil) before every byte it delivers, exercising the retry
// loop in readByte.
type stutterReader struct {
	data  []byte
	ready bool
}

func (r *stutterReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	if !r.ready {
		r.ready = true
		return 0, nil
	}
	r.ready = false
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func tempFile(t *testing.T) *os.File {
	f, err := os.Create(filepath.Join(t.TempDir(), "not-a-tty"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	return f
}

func TestMakeRawNotATerminal(t *testing.T) {
	restore, ok, err := makeRaw(tempFile(t))
	require.NoError(t, err)
	require.False(t, ok)
	require.NotNil(t, restore)
	require.NotPanics(t, restore)
}

func TestIsTerminalRegularFile(t *testing.T) {
	require.False(t, isTerminal(tempFile(t)))
}

func TestTerminalUIStyling(t *testing.T) {
	styled := &terminalUI{styled: true}
	require.Equal(t, "\x1b[2mx\x1b[0m", styled.dim("x"))
	require.Equal(t, "\x1b[1mx\x1b[0m", styled.bold("x"))
	require.Equal(t, "\x1b[36mx\x1b[0m", styled.cyan("x"))
	require.Equal(t, "\x1b[33mx\x1b[0m", styled.sgr("33", "x"))

	plain := &terminalUI{}
	require.Equal(t, "x", plain.dim("x"))
	require.Equal(t, "x", plain.bold("x"))
	require.Equal(t, "x", plain.cyan("x"))
	require.Equal(t, "x", plain.sgr("33", "x"))
}

func TestTerminalUIPrintf(t *testing.T) {
	ui, out := newTestUI("")
	ui.printf("a %s\nb\n", "x")
	require.Equal(t, "a x\r\nb\r\n", out.String())
}

func TestTerminalUIReadByte(t *testing.T) {
	ui := &terminalUI{in: &stutterReader{data: []byte("ab")}, out: io.Discard}
	for _, want := range []byte("ab") {
		c, err := ui.readByte()
		require.NoError(t, err)
		require.Equal(t, want, c)
	}
	_, err := ui.readByte()
	require.ErrorIs(t, err, io.EOF)
}

func TestTerminalUISkipEscapeSequence(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    byte
		wantErr bool
	}{
		{name: "csi", input: "[A", want: 'A'},
		{name: "not csi", input: "OA", want: 0},
		{name: "truncated", input: "[", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, _ := newTestUI(tc.input)
			got, err := ui.skipEscapeSequence()
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTerminalUISelectMany(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []int
	}{
		{name: "first item", input: " " + keyEnter, want: []int{0}},
		{name: "j moves down", input: "j " + keyEnter, want: []int{1}},
		{name: "k moves up", input: "jjk " + keyEnter, want: []int{1}},
		{name: "arrows move", input: keyDown + keyDown + keyUp + " " + keyEnter, want: []int{1}},
		{name: "clamped at top", input: "kk" + keyUp + " " + keyEnter, want: []int{0}},
		{name: "clamped at bottom", input: "jjjj" + keyDown + " " + keyEnter, want: []int{2}},
		{name: "toggle off", input: "  j " + keyEnter, want: []int{1}},
		{name: "presentation order", input: "jj k k " + keyEnter, want: []int{0, 1, 2}},
		{name: "newline confirms", input: " \n", want: []int{0}},
		{name: "unknown keys ignored", input: "xj\x01" + keyRight + "\x1bOA " + keyEnter, want: []int{1}},
		{name: "empty selection re-asks", input: keyEnter + "j " + keyEnter, want: []int{1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, _ := newTestUI(tc.input)
			got, err := ui.selectMany("Pick", testItems)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTerminalUISelectManyAbort(t *testing.T) {
	for _, input := range []string{"q", "\x03", "\x04", "j\x1b", "j\x1b["} {
		ui, _ := newTestUI(input)
		_, err := ui.selectMany("Pick", testItems)
		require.ErrorIs(t, err, errSetupAborted, "input %q", input)
	}
}

func TestTerminalUISelectManyInputExhausted(t *testing.T) {
	ui, _ := newTestUI("j ")
	_, err := ui.selectMany("Pick", testItems)
	require.ErrorIs(t, err, io.EOF)
}

func TestTerminalUISelectManyRender(t *testing.T) {
	t.Run("empty selection hint", func(t *testing.T) {
		ui, out := newTestUI(keyEnter + " " + keyEnter)
		_, err := ui.selectMany("Pick", testItems)
		require.NoError(t, err)
		require.Contains(t, out.String(), "Select at least one provider with space.")
	})

	t.Run("plain", func(t *testing.T) {
		ui, out := newTestUI("j " + keyEnter)
		_, err := ui.selectMany("Pick", testItems)
		require.NoError(t, err)

		// Initial render, one redraw after "j", one after " ", and the final render.
		frames := strings.Split(out.String(), "\x1b[")
		require.Len(t, frames, 1+2*3) // Each redraw starts with "\x1b[<n>A\x1b[J".

		first := frames[0]
		require.Equal(t, "  Pick\r\n"+
			"  ↑↓/jk move · space toggles · enter confirms · q aborts\r\n"+
			"\r\n"+
			"> [ ] one    first\r\n"+
			"  [ ] two    second\r\n"+
			"  [ ] three  third\r\n"+
			"\r\n"+
			"  \r\n", first)

		// Eight lines were drawn, so every redraw moves up eight lines and clears.
		require.Equal(t, 3, strings.Count(out.String(), "\x1b[8A\x1b[J"))

		final := out.String()[strings.LastIndex(out.String(), "\x1b[J")+len("\x1b[J"):]
		require.Equal(t, "  Pick\r\n\r\n  [x] two    second\r\n", final)
		require.NotContains(t, final, "space toggles")

		for _, line := range strings.SplitAfter(out.String(), "\n") {
			if line != "" {
				require.True(t, strings.HasSuffix(line, "\r\n"), "line %q", line)
			}
		}
	})

	t.Run("styled", func(t *testing.T) {
		ui, out := newTestUI(" " + keyEnter)
		ui.styled = true
		_, err := ui.selectMany("Pick", testItems)
		require.NoError(t, err)
		require.Contains(t, out.String(), "\x1b[1mPick\x1b[0m")
		require.Contains(t, out.String(), "\x1b[36m> \x1b[0m")
		require.Contains(t, out.String(), "\x1b[36m[x]\x1b[0m")
		require.Contains(t, out.String(), "\x1b[2mfirst\x1b[0m")
	})
}

func TestTerminalUIInputCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		def   string
		want  string
	}{
		{name: "typed", input: "hello" + keyEnter, want: "hello"},
		{name: "newline confirms", input: "hello\n", want: "hello"},
		{name: "default", input: keyEnter, def: "dflt", want: "dflt"},
		{name: "typed overrides default", input: "x" + keyEnter, def: "dflt", want: "x"},
		{name: "trimmed", input: "  hi  " + keyEnter, want: "hi"},
		{name: "only spaces uses default", input: "   " + keyEnter, def: "dflt", want: "dflt"},
		{name: "backspace", input: "abc\x7f" + keyEnter, want: "ab"},
		{name: "ctrl-h", input: "abc\x08\x08" + keyEnter, want: "a"},
		{name: "backspace multibyte rune", input: "aé€\x7f\x7f" + keyEnter, want: "a"},
		{name: "backspace on empty", input: "\x7f\x7fa\x7f\x7fb" + keyEnter, want: "b"},
		{name: "control chars ignored", input: "a\x01\x02\tb" + keyEnter, want: "ab"},
		{name: "arrows ignored", input: "a" + keyUp + keyDown + "\x1bOAb" + keyEnter, want: "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, _ := newTestUI(tc.input)
			got, err := ui.input("Label", tc.def, false)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTerminalUIInputErrors(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{name: "ctrl-c", input: "ab\x03", wantErr: errSetupAborted},
		{name: "ctrl-d", input: "\x04", wantErr: errSetupAborted},
		{name: "truncated escape", input: "a\x1b[", wantErr: errSetupAborted},
		{name: "input exhausted", input: "abc", wantErr: io.EOF},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, _ := newTestUI(tc.input)
			_, err := ui.input("Label", "", false)
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestTerminalUIInputRender(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		ui, out := newTestUI("ab" + keyEnter)
		_, err := ui.input("Name", "", false)
		require.NoError(t, err)
		require.Equal(t, "\r\x1b[K    Name: "+
			"\r\x1b[K    Name: a"+
			"\r\x1b[K    Name: ab"+
			"\r\n", out.String())
	})

	t.Run("default shown", func(t *testing.T) {
		ui, out := newTestUI(keyEnter)
		ui.styled = true
		_, err := ui.input("URL", "http://x", false)
		require.NoError(t, err)
		require.Contains(t, out.String(), "URL \x1b[2m[http://x]\x1b[0m: ")
	})

	t.Run("abort ends the line", func(t *testing.T) {
		ui, out := newTestUI("\x03")
		_, err := ui.input("Name", "", false)
		require.ErrorIs(t, err, errSetupAborted)
		require.True(t, strings.HasSuffix(out.String(), "\r\n"))
	})
}

func TestTerminalUIInputSecret(t *testing.T) {
	t.Run("masked", func(t *testing.T) {
		ui, out := newTestUI("s3cr€t" + keyEnter)
		got, err := ui.input("Key", "", true)
		require.NoError(t, err)
		require.Equal(t, "s3cr€t", got)
		require.NotContains(t, out.String(), "s3cr")
		require.NotContains(t, out.String(), "€")
		require.Contains(t, out.String(), strings.Repeat("•", 6)+"  (6 characters)")
		// Nothing typed yet: no count is shown.
		require.True(t, strings.HasPrefix(out.String(), "\r\x1b[K    Key: \r"))
	})

	t.Run("mask capped", func(t *testing.T) {
		secret := strings.Repeat("x", 40)
		ui, out := newTestUI(secret + keyEnter)
		got, err := ui.input("Key", "", true)
		require.NoError(t, err)
		require.Equal(t, secret, got)
		require.Contains(t, out.String(), strings.Repeat("•", 24)+"  (40 characters)")
		require.NotContains(t, out.String(), strings.Repeat("•", 25))
		require.NotContains(t, out.String(), "xx")
	})
}

func TestTerminalUIConfirm(t *testing.T) {
	tests := []struct {
		name  string
		input string
		def   bool
		want  bool
	}{
		{name: "y", input: "y" + keyEnter, want: true},
		{name: "Y", input: "Y" + keyEnter, want: true},
		{name: "yes", input: "yes" + keyEnter, want: true},
		{name: "YeS", input: "YeS" + keyEnter, want: true},
		{name: "n", input: "n" + keyEnter, def: true, want: false},
		{name: "N", input: "N" + keyEnter, def: true, want: false},
		{name: "no", input: "no" + keyEnter, def: true, want: false},
		{name: "NO", input: "NO" + keyEnter, def: true, want: false},
		{name: "empty default true", input: keyEnter, def: true, want: true},
		{name: "empty default false", input: keyEnter, def: false, want: false},
		{name: "invalid re-asks", input: "maybe" + keyEnter + "y" + keyEnter, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ui, _ := newTestUI(tc.input)
			got, err := ui.confirm("Continue?", tc.def)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestTerminalUIConfirmHint(t *testing.T) {
	ui, out := newTestUI(keyEnter)
	_, err := ui.confirm("Continue?", true)
	require.NoError(t, err)
	require.Contains(t, out.String(), "Continue? (Y/n): ")

	ui, out = newTestUI(keyEnter)
	_, err = ui.confirm("Continue?", false)
	require.NoError(t, err)
	require.Contains(t, out.String(), "Continue? (y/N): ")

	// An invalid answer re-asks the question.
	ui, out = newTestUI("maybe" + keyEnter + keyEnter)
	_, err = ui.confirm("Continue?", false)
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(out.String(), "\r\x1b[K    Continue? (y/N): \r"))
}

func TestTerminalUIConfirmAbort(t *testing.T) {
	for _, input := range []string{"\x03", "y\x04", "maybe" + keyEnter + "\x03", ""} {
		ui, _ := newTestUI(input)
		_, err := ui.confirm("Continue?", true)
		require.Error(t, err, "input %q", input)
	}
	ui, _ := newTestUI("\x03")
	_, err := ui.confirm("Continue?", true)
	require.ErrorIs(t, err, errSetupAborted)
}
