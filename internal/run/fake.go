package run

import (
	"context"
	"strings"
	"sync"
)

// Call records one invocation against a FakeRunner.
type Call struct {
	Kind    string // "exec" | "start" | "up" | "down"
	Service string
	Argv    []string
}

// FakeRunner is an in-memory Runner for unit tests. It records every call and
// returns canned output/errors keyed by a substring of the joined argv.
type FakeRunner struct {
	mu      sync.Mutex
	Calls   []Call
	Outputs map[string]string // argv-substring → stdout
	Errors  map[string]error  // argv-substring → error
}

func NewFake() *FakeRunner {
	return &FakeRunner{Outputs: map[string]string{}, Errors: map[string]error{}}
}

func (f *FakeRunner) record(c Call) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, c)
}

func (f *FakeRunner) lookup(argv []string) (string, error) {
	joined := strings.Join(argv, " ")
	for sub, err := range f.Errors {
		if strings.Contains(joined, sub) {
			return f.Outputs[sub], err
		}
	}
	for sub, out := range f.Outputs {
		if strings.Contains(joined, sub) {
			return out, nil
		}
	}
	return "", nil
}

func (f *FakeRunner) Exec(_ context.Context, service string, argv ...string) (string, error) {
	f.record(Call{Kind: "exec", Service: service, Argv: argv})
	return f.lookup(argv)
}

func (f *FakeRunner) Start(_ context.Context, service string, argv ...string) (*Handle, error) {
	f.record(Call{Kind: "start", Service: service, Argv: argv})
	h := &Handle{done: make(chan error, 1)}
	h.done <- nil
	return h, nil
}

func (f *FakeRunner) ComposeUp(context.Context) error {
	f.record(Call{Kind: "up"})
	return nil
}

func (f *FakeRunner) ComposeDown(context.Context) error {
	f.record(Call{Kind: "down"})
	return nil
}

// ExecCalls returns only the exec/start calls (convenience for assertions).
func (f *FakeRunner) ExecCalls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.Calls {
		if c.Kind == "exec" || c.Kind == "start" {
			out = append(out, c)
		}
	}
	return out
}
