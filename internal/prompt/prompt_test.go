package prompt

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

type countingReader struct {
	reader io.Reader
	bytes  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.bytes += n
	return n, err
}

func TestRequiredLineAndNonTTYSecret(t *testing.T) {
	var out bytes.Buffer
	p := New(strings.NewReader("  tést@example.com  \n123456\n"), &out)
	email, err := p.RequiredLine("Email: ", 254)
	if err != nil || email != "tést@example.com" {
		t.Fatalf("email=%q err=%v", email, err)
	}
	secret, err := p.Secret("Code: ", 6)
	if err != nil || secret != "123456" {
		t.Fatalf("secret=%q err=%v", secret, err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("secret was echoed")
	}
}

func TestSecretUsesInjectedHiddenTTYReader(t *testing.T) {
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = read.Close() }()
	defer func() { _ = write.Close() }()
	var out bytes.Buffer
	p := New(read, &out)
	p.isTerminal = func(int) bool { return true }
	p.readPassword = func(int) ([]byte, error) { return []byte("654321"), nil }
	secret, err := p.Secret("Code: ", 6)
	if err != nil || secret != "654321" {
		t.Fatalf("secret=%q err=%v", secret, err)
	}
	if strings.Contains(out.String(), secret) {
		t.Fatal("TTY secret was echoed")
	}
}

func TestPromptsRejectEOFWhitespaceAndByteOverflow(t *testing.T) {
	for name, input := range map[string]string{"eof": "", "whitespace": "   \n", "overflow": "ééé\n"} {
		t.Run(name, func(t *testing.T) {
			p := New(strings.NewReader(input), &bytes.Buffer{})
			if _, err := p.RequiredLine("", 4); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestPromptsRejectLargeUnterminatedInputWithoutAccumulatingIt(t *testing.T) {
	input := &countingReader{reader: strings.NewReader(strings.Repeat("x", 1<<20))}
	p := New(input, &bytes.Buffer{})
	if _, err := p.RequiredLine("", 4); err == nil {
		t.Fatal("expected input-too-long error")
	}
	if input.bytes > 16*1024 {
		t.Fatalf("read %d bytes before rejecting bounded input", input.bytes)
	}
}
