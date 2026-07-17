package status

import (
	"fmt"
	"os"
	"time"
)

// Live drives the dashboard. On a TTY it redraws the block in place ~11×/sec so
// the spinner animates and values change smoothly; otherwise it prints a plain
// status line every few seconds.
type Live struct {
	model *Model
	w     *os.File
	tty   bool
	stop  chan struct{}
	done  chan struct{}
	lines int
}

// NewLive builds a Live renderer targeting stderr (so stdout stays clean for the
// final report).
func NewLive(m *Model) *Live {
	return &Live{
		model: m, w: os.Stderr, tty: isTTY(os.Stderr),
		stop: make(chan struct{}), done: make(chan struct{}),
	}
}

// TTY reports whether the dashboard is animating (vs plain fallback).
func (l *Live) TTY() bool { return l.tty }

// Start begins rendering in a background goroutine.
func (l *Live) Start() { go l.loop() }

// Stop draws a final frame and returns once the renderer has cleaned up.
func (l *Live) Stop() {
	select {
	case <-l.done:
		return // already stopped
	default:
	}
	close(l.stop)
	<-l.done
}

func (l *Live) loop() {
	defer close(l.done)
	if !l.tty {
		l.loopPlain()
		return
	}
	fmt.Fprint(l.w, "\033[?25l")       // hide cursor
	defer fmt.Fprint(l.w, "\033[?25h") // show cursor
	t := time.NewTicker(90 * time.Millisecond)
	defer t.Stop()
	l.render(false)
	for {
		select {
		case <-l.stop:
			l.render(true)
			return
		case <-t.C:
			l.model.tick()
			l.render(false)
		}
	}
}

// render redraws the block in place: move the cursor up to the block's top, then
// rewrite each line (clearing any leftovers).
func (l *Live) render(final bool) {
	lines := l.model.lines(final)
	if l.lines > 0 {
		fmt.Fprintf(l.w, "\033[%dA", l.lines)
	}
	for _, line := range lines {
		fmt.Fprintf(l.w, "\033[2K%s\n", line)
	}
	l.lines = len(lines)
}

func (l *Live) loopPlain() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	i := 0
	for {
		select {
		case <-l.stop:
			fmt.Fprintln(l.w, l.model.plainLine())
			return
		case <-t.C:
			if i%5 == 0 { // ~every 5s
				fmt.Fprintln(l.w, l.model.plainLine())
			}
			i++
		}
	}
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
