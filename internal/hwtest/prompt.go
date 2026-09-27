//go:build hwtest

package hwtest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// Prompter is the user at the keyboard. Every request carries an id, so a
// test can script the answers.
type Prompter interface {
	// Say shows text.
	Say(text string)
	// Wait shows an instruction and returns once the user pressed Enter.
	Wait(id, text string) error
	// Ask asks a yes/no question.
	Ask(id, text string) (bool, error)
	// Line asks for a line of text, such as a typed confirmation.
	Line(id, text string) (string, error)
}

// ErrNoInput is the end of the user's input.
var ErrNoInput = errors.New("hwtest: no more input")

// Terminal reads the user's answers from in, a line each, and writes to out.
// The terminal echoes what the user types; nothing is read with echo off.
func Terminal(in io.Reader, out io.Writer) Prompter {
	return &terminal{in: bufio.NewScanner(in), out: out}
}

type terminal struct {
	mu  sync.Mutex
	in  *bufio.Scanner
	out io.Writer
}

func (t *terminal) Say(text string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprintln(t.out, text)
}

func (t *terminal) Wait(_, text string) error {
	_, err := t.line(text + " [Enter] ")
	return err
}

func (t *terminal) Ask(_, text string) (bool, error) {
	for {
		s, err := t.line(text + " [y/n] ")
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
	}
}

func (t *terminal) Line(_, text string) (string, error) {
	return t.line(text + " ")
}

func (t *terminal) line(prompt string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fmt.Fprint(t.out, prompt)
	if !t.in.Scan() {
		fmt.Fprintln(t.out)
		if err := t.in.Err(); err != nil {
			return "", err
		}
		return "", ErrNoInput
	}
	return t.in.Text(), nil
}
