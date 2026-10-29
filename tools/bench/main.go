// Command bench measures the same operations on a Rex server, on herdrex in
// front of Herdr, and on Herdr itself, so that the three paths can be
// compared (docs/TESTING.md). It creates one session or workspace on each
// target and closes it at the end; point it only at servers started for it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
	"text/tabwriter"
	"time"
)

var (
	rexURL    = flag.String("rex", "", "Rex server URL, such as http://127.0.0.1:7298")
	bridgeURL = flag.String("bridge", "", "herdrex URL, such as http://127.0.0.1:7397")
	herdrSock = flag.String("herdr", "", "Herdr API socket; with -bridge, the Herdr behind it")
	n         = flag.Int("n", 200, "samples per read and per key")
	ops       = flag.Int("ops", 30, "samples per change")
	lines     = flag.Int("lines", 100000, "lines that seq prints for the output test")
	timeout   = flag.Duration("timeout", 10*time.Second, "bound on each wait")
	verbose   = flag.Bool("v", false, "also print n, p99 and max of every measure")
)

// marker is what the shell prints when the output test ends. The command
// line holds $((40+2)), so its echo does not match.
const marker = "DONE42"

// shellStart is how long a new pane gets before it is closed. Herdr closes a
// pane whose shell has not started yet in 0.3 ms and one whose shell runs in
// about 30 ms, so closing at once would time the case no user meets.
const shellStart = 100 * time.Millisecond

func main() {
	flag.Parse()
	ctx := context.Background()
	var all []*results
	run := func(target string, f func(context.Context, *results) error) {
		r := newResults(target)
		r.note("setup", f(ctx, r))
		all = append(all, r)
	}
	if *rexURL != "" {
		run("rex", func(ctx context.Context, r *results) error { return rexBench(ctx, r, *rexURL, "") })
	}
	if *bridgeURL != "" {
		run("herdrex", func(ctx context.Context, r *results) error { return rexBench(ctx, r, *bridgeURL, *herdrSock) })
	}
	if *herdrSock != "" {
		run("herdr", func(ctx context.Context, r *results) error { return herdrBench(ctx, r, *herdrSock) })
	}
	compare(all)
	if *verbose {
		for _, r := range all {
			r.detail()
		}
	}
	for _, r := range all {
		for _, s := range r.info {
			fmt.Printf("%s: %s\n", r.target, s)
		}
	}
}

// results are one target's measures, in the order they were first taken.
type results struct {
	target string
	order  []string
	d      map[string]durations
	info   []string // counts and failures, printed after the tables
}

func newResults(target string) *results {
	return &results{
		target: target,
		d:      map[string]durations{},
	}
}

func (r *results) add(name string, d time.Duration) {
	if _, ok := r.d[name]; !ok {
		r.order = append(r.order, name)
	}
	r.d[name] = append(r.d[name], d)
}

// note records err, if any, against what, and says whether there was none.
// A failed measure is left out; the others still run.
func (r *results) note(what string, err error) bool {
	if err == nil {
		return true
	}
	r.info = append(r.info, fmt.Sprintf("%s failed: %v", what, err))
	log.Printf("%s: %s: %v", r.target, what, err)
	return false
}

// timed runs f k times into the named measure and stops at the first error.
func (r *results) timed(name string, k int, f func() error) {
	r.timedEach(name, k, func(int) error { return f() })
}

// timedEach is timed with the sample's index passed to f.
func (r *results) timedEach(name string, k int, f func(i int) error) {
	for i := range k {
		t0 := time.Now()
		if err := f(i); !r.note(name, err) {
			return
		}
		r.add(name, time.Since(t0))
	}
}

// compare prints p50 and p90 of every measure, a column pair per target.
func compare(all []*results) {
	var names []string
	for _, r := range all {
		for _, name := range r.order {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprint(w, "measure\t")
	for _, r := range all {
		fmt.Fprintf(w, "%s p50\tp90\t", r.target)
	}
	fmt.Fprintln(w)
	for _, name := range names {
		fmt.Fprintf(w, "%s\t", name)
		for _, r := range all {
			d := r.d[name]
			fmt.Fprintf(w, "%s\t%s\t", d.at(0.5), d.at(0.9))
		}
		fmt.Fprintln(w)
	}
	w.Flush()
}

// detail prints every measure of one target with n, p99 and max.
func (r *results) detail() {
	fmt.Printf("\n%s\n", r.target)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(w, "measure\tn\tp50\tp90\tp99\tmax\t")
	for _, name := range r.order {
		d := r.d[name]
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%s\t\n", name, len(d), d.at(0.5), d.at(0.9), d.at(0.99), d.at(1))
	}
	w.Flush()
}

type durations []time.Duration

// at is the q quantile, rounded for reading.
func (d durations) at(q float64) string {
	if len(d) == 0 {
		return "-"
	}
	s := slices.Sorted(slices.Values(d))
	v := s[min(len(s)-1, int(q*float64(len(s))))]
	switch {
	case v >= 100*time.Millisecond:
		return v.Round(time.Millisecond).String()
	case v >= time.Millisecond:
		return v.Round(10 * time.Microsecond).String()
	}
	return v.Round(time.Microsecond).String()
}

// output test: the command that prints the lines and then the marker.
func seqCommand() string {
	return fmt.Sprintf("seq 1 %d; echo DONE$((40+2))\r", *lines)
}

// keyOf is the i-th typed key: letters only, so that it cannot be part of
// an escape sequence's parameters.
func keyOf(i int) string { return string(rune('a' + i%26)) }
