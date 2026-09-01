package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type Prompter struct {
	in           io.Reader
	out          io.Writer
	reader       *bufio.Reader
	readPassword func(int) ([]byte, error)
	isTerminal   func(int) bool
}

var errInputTooLong = errors.New("input is too long")

// readBoundedLine reads at most maxBytes of content plus the terminating
// newline. It stops as soon as the limit is exceeded so unterminated input
// cannot be accumulated without bound.
func readBoundedLine(reader *bufio.Reader, maxBytes int) (string, error) {
	var value []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		value = append(value, fragment...)
		if len(value) > maxBytes+1 {
			return "", errInputTooLong
		}
		if err == nil {
			return string(value), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return string(value), io.EOF
		}
		return "", err
	}
}

func New(in io.Reader, out io.Writer) *Prompter {
	p := &Prompter{in: in, out: out, reader: bufio.NewReader(in), isTerminal: term.IsTerminal}
	p.readPassword = term.ReadPassword
	return p
}

func (p *Prompter) RequiredLine(label string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("invalid input limit")
	}
	_, _ = fmt.Fprint(p.out, label)
	line, err := readBoundedLine(p.reader, maxBytes)
	if errors.Is(err, errInputTooLong) {
		return "", errInputTooLong
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("unable to read input")
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return "", errors.New("input is required")
	}
	if len([]byte(value)) > maxBytes {
		return "", errors.New("input is too long")
	}
	return value, nil
}

func (p *Prompter) Secret(label string, maxBytes int) (string, error) {
	if maxBytes <= 0 {
		return "", errors.New("invalid input limit")
	}
	_, _ = fmt.Fprint(p.out, label)
	var raw []byte
	var err error
	if file, ok := p.in.(*os.File); ok && p.isTerminal(int(file.Fd())) {
		raw, err = p.readPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(p.out)
	} else {
		var line string
		line, err = readBoundedLine(p.reader, maxBytes)
		raw = []byte(line)
	}
	if errors.Is(err, errInputTooLong) {
		return "", errInputTooLong
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("unable to read secret")
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", errors.New("input is required")
	}
	if len([]byte(value)) > maxBytes {
		return "", errors.New("input is too long")
	}
	return value, nil
}
