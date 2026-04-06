package display

import (
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

var frames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner provides a TTY-aware progress spinner on stderr.
type Spinner struct {
	mu      sync.Mutex
	msg     string
	active  bool
	stop    chan struct{}
	done    chan struct{}
	isTTY   bool
}

// New creates a spinner. Animation auto-disables on non-TTY stderr.
func New() *Spinner {
	return &Spinner{
		isTTY: term.IsTerminal(int(os.Stderr.Fd())),
	}
}

// Start begins the spinner with an initial message.
func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return
	}
	s.msg = msg
	s.active = true
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	s.mu.Unlock()

	if !s.isTTY {
		fmt.Fprintf(os.Stderr, "%s\n", msg)
		return
	}

	go s.run()
}

// Update changes the spinner message.
func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()

	if !s.isTTY {
		fmt.Fprintf(os.Stderr, "%s\n", msg)
	}
}

// Stop halts the spinner and clears the line.
func (s *Spinner) Stop() {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return
	}
	s.active = false
	s.mu.Unlock()

	if s.isTTY {
		close(s.stop)
		<-s.done
		fmt.Fprintf(os.Stderr, "\r\033[K")
	}
}

// StopWithMessage halts and prints a final message.
func (s *Spinner) StopWithMessage(msg string) {
	s.Stop()
	fmt.Fprintf(os.Stderr, "%s\n", msg)
}

func (s *Spinner) run() {
	defer close(s.done)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	i := 0
	for {
		select {
		case <-s.stop:
			return
		case <-tick.C:
			s.mu.Lock()
			msg := s.msg
			s.mu.Unlock()
			fmt.Fprintf(os.Stderr, "\r\033[K%s %s", frames[i%len(frames)], msg)
			i++
		}
	}
}
