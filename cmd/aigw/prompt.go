// Copyright Envoy AI Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// errSetupAborted is returned when the user abandons the interactive setup.
var errSetupAborted = errors.New("setup aborted")

const (
	keyCtrlC     = 0x03
	keyCtrlD     = 0x04
	keyBackspace = 0x7f
	keyCtrlH     = 0x08
	keyEscape    = 0x1b
)

// terminalUI draws prompts on a terminal in raw mode. Raw mode is needed to read single
// keypresses for the checklist, and it also means Ctrl-C arrives as a byte rather than a
// signal, so every prompt can abort cleanly and leave the terminal as it was.
type terminalUI struct {
	in     io.Reader
	out    io.Writer
	styled bool
}

// makeRaw puts f into raw mode, returning a function that restores the previous state.
// It reports whether f is a terminal at all.
func makeRaw(f *os.File) (restore func(), ok bool, err error) {
	fd := int(f.Fd()) //nolint:gosec // a file descriptor always fits in an int.
	if !term.IsTerminal(fd) {
		return func() {}, false, nil
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, false, fmt.Errorf("failed to switch terminal to raw mode: %w", err)
	}
	return func() { _ = term.Restore(fd, state) }, true, nil
}

// isTerminal reports whether f is a terminal. It is a variable so tests can override it.
var isTerminal = func(f *os.File) bool {
	return term.IsTerminal(int(f.Fd())) //nolint:gosec // a file descriptor always fits in an int.
}

func (u *terminalUI) sgr(code, s string) string {
	if !u.styled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (u *terminalUI) dim(s string) string  { return u.sgr("2", s) }
func (u *terminalUI) bold(s string) string { return u.sgr("1", s) }
func (u *terminalUI) cyan(s string) string { return u.sgr("36", s) }

// printf writes to the terminal. In raw mode the terminal does not add a carriage
// return on a newline, so every "\n" is written as "\r\n".
func (u *terminalUI) printf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	_, _ = io.WriteString(u.out, strings.ReplaceAll(s, "\n", "\r\n"))
}

func (u *terminalUI) readByte() (byte, error) {
	buf := make([]byte, 1)
	for {
		n, err := u.in.Read(buf)
		if err != nil {
			return 0, err
		}
		if n == 1 {
			return buf[0], nil
		}
	}
}

// skipEscapeSequence discards the rest of an escape sequence such as an arrow key.
func (u *terminalUI) skipEscapeSequence() (byte, error) {
	seq := make([]byte, 2)
	if _, err := io.ReadFull(u.in, seq); err != nil {
		return 0, err
	}
	if seq[0] != '[' {
		return 0, nil
	}
	return seq[1], nil
}

// selectItem is one row of a checklist.
type selectItem struct {
	label  string
	detail string
}

// selectMany draws items as a checklist and returns the indices the user confirmed,
// in the order they were presented. At least one item must be selected.
func (u *terminalUI) selectMany(title string, items []selectItem) ([]int, error) {
	selected := make([]bool, len(items))
	cursor, drawn := 0, 0
	hint := ""

	width := 0
	for _, it := range items {
		width = max(width, utf8.RuneCountInString(it.label))
	}

	render := func(final bool) {
		var b strings.Builder
		if drawn > 0 {
			// Return to the top of the previous drawing and clear from there down.
			fmt.Fprintf(&b, "\x1b[%dA\x1b[J", drawn)
		}
		lines := 0
		writeln := func(s string) {
			b.WriteString(s + "\r\n")
			lines++
		}
		writeln("  " + u.bold(title))
		if !final {
			writeln("  " + u.dim("↑↓/jk move · space toggles · enter confirms · q aborts"))
		}
		writeln("")
		for i, it := range items {
			if final && !selected[i] {
				continue
			}
			pointer := "  "
			if i == cursor && !final {
				pointer = u.cyan("> ")
			}
			box := "[ ]"
			if selected[i] {
				box = u.cyan("[x]")
			}
			pad := strings.Repeat(" ", width-utf8.RuneCountInString(it.label))
			writeln(fmt.Sprintf("%s%s %s%s  %s", pointer, box, it.label, pad, u.dim(it.detail)))
		}
		if !final {
			writeln("")
			writeln("  " + u.sgr("33", hint))
		}
		_, _ = io.WriteString(u.out, b.String())
		drawn = lines
	}

	render(false)
	for {
		c, err := u.readByte()
		if err != nil {
			return nil, err
		}
		switch c {
		case 'k':
			cursor = max(cursor-1, 0)
		case 'j':
			cursor = min(cursor+1, len(items)-1)
		case ' ':
			selected[cursor] = !selected[cursor]
			hint = ""
		case '\r', '\n':
			var chosen []int
			for i, ok := range selected {
				if ok {
					chosen = append(chosen, i)
				}
			}
			if len(chosen) == 0 {
				hint = "Select at least one provider with space."
				break
			}
			render(true)
			return chosen, nil
		case 'q', keyCtrlC, keyCtrlD:
			return nil, errSetupAborted
		case keyEscape:
			key, err := u.skipEscapeSequence()
			if err != nil {
				return nil, errSetupAborted
			}
			switch key {
			case 'A':
				cursor = max(cursor-1, 0)
			case 'B':
				cursor = min(cursor+1, len(items)-1)
			}
		default:
			continue
		}
		render(false)
	}
}

// input reads a line of text. Secret input is masked as it is typed or pasted.
// An empty answer returns def.
func (u *terminalUI) input(label, def string, secret bool) (string, error) {
	var value []byte
	render := func() {
		shown := string(value)
		if secret {
			n := utf8.RuneCount(value)
			shown = strings.Repeat("•", min(n, 24))
			if n > 0 {
				shown += u.dim(fmt.Sprintf("  (%d characters)", n))
			}
		}
		prompt := label + ": "
		if def != "" {
			prompt = label + " " + u.dim("["+def+"]") + ": "
		}
		// Redraw the whole line, so masking and backspace work the same way.
		_, _ = io.WriteString(u.out, "\r\x1b[K    "+prompt+shown)
	}

	render()
	for {
		c, err := u.readByte()
		if err != nil {
			return "", err
		}
		switch c {
		case '\r', '\n':
			_, _ = io.WriteString(u.out, "\r\n")
			if v := strings.TrimSpace(string(value)); v != "" {
				return v, nil
			}
			return def, nil
		case keyCtrlC, keyCtrlD:
			_, _ = io.WriteString(u.out, "\r\n")
			return "", errSetupAborted
		case keyBackspace, keyCtrlH:
			if len(value) > 0 {
				_, size := utf8.DecodeLastRune(value)
				value = value[:len(value)-size]
			}
		case keyEscape:
			if _, err := u.skipEscapeSequence(); err != nil {
				return "", errSetupAborted
			}
			continue
		default:
			if c < 0x20 {
				continue // Ignore other control characters.
			}
			value = append(value, c)
		}
		render()
	}
}

// confirm asks a yes/no question.
func (u *terminalUI) confirm(label string, def bool) (bool, error) {
	choices := "y/N"
	if def {
		choices = "Y/n"
	}
	for {
		answer, err := u.input(label+" "+u.dim("("+choices+")"), "", false)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(answer) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}
