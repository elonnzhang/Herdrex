package main

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// terminal is a pane's input and the output it streams back.
type terminal struct {
	out   <-chan []byte
	write func(string) error
}

// settle waits for output to go quiet, as after a shell's first prompt.
func (t *terminal) settle() error {
	quiet := time.NewTimer(300 * time.Millisecond)
	defer quiet.Stop()
	deadline := time.After(*timeout)
	for {
		select {
		case _, ok := <-t.out:
			if !ok {
				return fmt.Errorf("output ended")
			}
			quiet.Reset(300 * time.Millisecond)
		case <-quiet.C:
			return nil
		case <-deadline:
			return fmt.Errorf("output did not settle within %v", *timeout)
		}
	}
}

// drain drops output until none has come for a short while, so that what
// the next write brings is not output of the one before.
func (t *terminal) drain() {
	for {
		select {
		case _, ok := <-t.out:
			if !ok {
				return
			}
		case <-time.After(30 * time.Millisecond):
			return
		}
	}
}

// until waits for output that contains want, keeping the last bytes so
// that want may be split between chunks.
func (t *terminal) until(want string) (int, error) {
	var tail []byte
	total := 0
	deadline := time.After(*timeout)
	for {
		select {
		case b, ok := <-t.out:
			if !ok {
				return total, fmt.Errorf("output ended before %q", want)
			}
			total += len(b)
			tail = append(tail, b...)
			if bytes.Contains(tail, []byte(want)) {
				return total, nil
			}
			if k := len(tail) - len(want); k > 0 {
				tail = tail[k:]
			}
		case <-deadline:
			return total, fmt.Errorf("no %q within %v", want, *timeout)
		}
	}
}

// timedOutput writes data and times it until the output shows want.
func (t *terminal) timedOutput(data, want string) (time.Duration, int, error) {
	t.drain()
	t0 := time.Now()
	if err := t.write(data); err != nil {
		return 0, 0, fmt.Errorf("write: %w", err)
	}
	total, err := t.until(want)
	return time.Since(t0), total, err
}

// measure records the output test, then, with cat running, the key echo
// and paste tests. Each test that fails is left out with its error logged.
func (t *terminal) measure(r *results, echoName string) {
	d, total, err := t.timedOutput(seqCommand(), marker)
	if r.note("seq", err) {
		name := fmt.Sprintf("seq 1 %d", *lines)
		r.add(name, d)
		r.info = append(r.info, fmt.Sprintf("%s: %d bytes of output", name, total))
	}
	t.withCat(r, func() {
		r.note(echoName, t.each(r, echoName, *n, t.key))
		r.note("paste 4 KiB", t.each(r, "paste 4 KiB", *ops, t.paste))
	})
}

// withCat runs f with cat in the foreground, so that typed text comes back.
func (t *terminal) withCat(r *results, f func()) {
	if err := t.write("cat\r"); r.note("start cat", err) {
		if r.note("start cat", t.settle()) {
			f()
		}
	}
	t.write("\x03")
	t.settle()
}

// each runs one timed test k times into the named measure.
func (t *terminal) each(r *results, name string, k int, f func(int) (time.Duration, error)) error {
	for i := range k {
		d, err := f(i)
		if err != nil {
			return err
		}
		r.add(name, d)
	}
	return nil
}

// key types one letter. Every 40 keys the line is ended so it stays short.
func (t *terminal) key(i int) (time.Duration, error) {
	if i%40 == 39 {
		t.write("\r")
		t.settle()
	}
	k := keyOf(i)
	d, _, err := t.timedOutput(k, k)
	return d, err
}

// paste writes 4 KiB of short lines, the last of which ends in a marker.
// Each sample's marker uses another letter, and the last line fits in a
// half-width pane, so that a redraw that sends only changed cells still
// sends the marker whole.
func (t *terminal) paste(i int) (time.Duration, error) {
	line := strings.Repeat("x", 63) + "\r"
	want := strings.Repeat(string(rune('A'+i%26)), 8)
	data := strings.Repeat(line, 63) + strings.Repeat("y", 24) + want + "\r"
	d, _, err := t.timedOutput(data, want)
	return d, err
}
