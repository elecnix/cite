package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/elecnix/cite/internal/config"
	"github.com/elecnix/cite/internal/model"
)

// duplicatedModelConfig declares the same model id under two providers with
// different rates. Two providers legitimately disagree about what a shared
// model id costs: one routes through a paid gateway, the other is a local
// endpoint that bills nothing.
func duplicatedModelConfig() *config.Config {
	entry := func(in, out float64) model.ModelEntry {
		return model.ModelEntry{ID: "openai/gpt-4o-mini", Cost: &model.Cost{Input: in, Output: out}}
	}
	return &config.Config{
		Providers: map[string]*model.Provider{
			"gateway": {Name: "gateway", Models: []model.ModelEntry{entry(5, 30)}},
			"local":   {Name: "local", Models: []model.ModelEntry{entry(0, 0)}},
		},
	}
}

// TestCostRatesForIsDeterministic pins the invariant that one model id and one
// config always resolve to one set of rates. The lookup scans a Go map, whose
// iteration order is randomised per range statement, so a model id declared
// under two providers resolved to whichever provider the runtime happened to
// visit first. The same run, replayed, could report a cost thirty times larger
// than the previous one.
func TestCostRatesForIsDeterministic(t *testing.T) {
	cfg := duplicatedModelConfig()
	seen := map[float64]bool{}
	for i := 0; i < 200; i++ {
		rate := costRatesFor("openai/gpt-4o-mini", cfg)
		if rate == nil {
			t.Fatal("costRatesFor returned no rates for a declared model")
		}
		seen[rate.Input] = true
	}
	if len(seen) > 1 {
		t.Errorf("costRatesFor resolved one model id to %d different rate sets %v; a cost report must not depend on map iteration order", len(seen), seen)
	}
}

// TestCostRatesForPrefersTheQualifiedReference checks the resolution order. A
// provider-qualified reference ("gateway/openai/gpt-4o-mini") names the
// provider, so its rates win over a bare id that any provider may claim.
func TestCostRatesForPrefersTheQualifiedReference(t *testing.T) {
	cfg := duplicatedModelConfig()
	for i := 0; i < 200; i++ {
		rate := costRatesFor("gateway/openai/gpt-4o-mini", cfg)
		if rate == nil {
			t.Fatal("costRatesFor returned no rates for a declared model")
		}
		if rate.Input != 5 {
			t.Fatalf("costRatesFor(\"gateway/openai/gpt-4o-mini\").Input = %v, want 5 (the named provider's rates)", rate.Input)
		}
	}
}

// captureStderrWait bounds how long captureStderr waits for the reader once
// the function under test has returned.
const captureStderrWait = 2 * time.Second

// captureStderr collects everything a function writes to os.Stderr. The cost
// report is printed on stdout by printRecord, which cannot be reached from
// here, so the mismatch note is written where every other Cite diagnostic goes.
//
// It always returns. Closing the write end is not enough to end the read, and
// that pipe is not this helper's to own: a subprocess that inherited the
// descriptor, or a duplicate nobody closes, keeps it open after the helper has
// done everything it can, and end-of-file never arrives. The wait is therefore
// bounded, and a pipe that outlives the function returns whatever reached the
// reader first. Closing the write end early, by contrast, ends the read at once
// and is not a hazard.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w

	var mu sync.Mutex
	var got bytes.Buffer
	read := make(chan struct{})
	go func() {
		defer close(read)
		b := make([]byte, 4096)
		for {
			n, err := r.Read(b)
			if n > 0 {
				mu.Lock()
				got.Write(b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	// A panic inside the function under test is how a real failure first
	// reaches this helper, and it unwinds straight past the lines below.
	// Restoring os.Stderr and closing both ends here keeps the test binary's
	// own stderr: a stream left pointing at this pipe makes every later test
	// write into a reader that is never drained.
	defer func() {
		os.Stderr = saved
		w.Close()
		r.Close()
	}()

	fn()
	os.Stderr = saved
	w.Close()

	timer := time.NewTimer(captureStderrWait)
	defer timer.Stop()
	select {
	case <-read:
	case <-timer.C:
		t.Logf("captureStderr: stderr was still open %v after the function returned; reporting what reached the reader", captureStderrWait)
	}
	r.Close()

	mu.Lock()
	defer mu.Unlock()
	return got.String()
}

// TestApplyCostWarnsWhenTheRunModelHasNoRates covers the silent zero. The model
// client reads MODEL_ID from the environment and never consults the providers
// block, so a repository whose config declares one set of models can easily run
// a model that block never declared. applyCost found no rates and returned 0,
// and the run then printed "cost: $0.0000" as though zero were the price.
func TestApplyCostWarnsWhenTheRunModelHasNoRates(t *testing.T) {
	rec := &model.RunRecord{Model: "openai/gpt-5-mini",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, testCostConfig()) })
	if rec.CostUSD != 0 {
		t.Errorf("CostUSD = %v, want 0 for a model with no declared rates", rec.CostUSD)
	}
	if out == "" {
		t.Fatal("applyCost reported no cost and said nothing; an absent rate is not a price of zero")
	}
}

// TestApplyCostStaysQuietWhenNoProvidersDeclared keeps the note narrow. With no
// providers block there are no declared rates anywhere, so a zero cost is the
// documented outcome and warning about it on every run would be noise.
func TestApplyCostStaysQuietWhenNoProvidersDeclared(t *testing.T) {
	rec := &model.RunRecord{Model: "openai/gpt-4o-mini",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, &config.Config{}) })
	if out != "" {
		t.Errorf("applyCost wrote %q with no providers declared; want silence", out)
	}
}

// TestApplyCostStaysQuietWhenRatesResolve keeps the note narrow in the other
// direction: a run whose model is declared and priced says nothing.
func TestApplyCostStaysQuietWhenRatesResolve(t *testing.T) {
	rec := &model.RunRecord{Model: "gateway/vendor/model-x",
		Usage: model.Usage{InputTokens: 45_000, OutputTokens: 2_000}}
	out := captureStderr(t, func() { applyCost(rec, testCostConfig()) })
	if rec.CostUSD == 0 {
		t.Fatalf("CostUSD = 0 for a declared, priced model")
	}
	if out != "" {
		t.Errorf("applyCost wrote %q for a run whose rates resolved; want silence", out)
	}
}

// --- regression tests for the captureStderr helper itself --------------------

// holdTheWriteEnd models whatever keeps a stderr pipe open after the function
// under test has returned: a subprocess that inherited the descriptor, or a
// duplicated file descriptor that was never closed. Both hold a second
// reference to the write end, so closing the helper's own reference does not
// produce end-of-file and the reader never returns.
func holdTheWriteEnd(t *testing.T, text string) {
	t.Helper()
	leak, err := syscall.Dup(int(os.Stderr.Fd()))
	if err != nil {
		t.Fatalf("dup: %v", err)
	}
	t.Cleanup(func() { syscall.Close(leak) })
	fmt.Fprint(os.Stderr, text)
}

// TestCaptureStderrReturnsWhenTheWriteEndStaysOpen is the regression test for
// the helper's one hard requirement: it returns. Before the fix, captureStderr
// closed the write end and then waited on an unbounded channel receive, so a
// pipe that never reached end-of-file blocked until the test binary's own
// timeout. The wait is run in a child process because a hang cannot be
// asserted from inside the hanging goroutine.
func TestCaptureStderrReturnsWhenTheWriteEndStaysOpen(t *testing.T) {
	if os.Getenv("CITE_CAPTURE_STDERR_CHILD") == "1" {
		got := captureStderr(t, func() { holdTheWriteEnd(t, "partial output") })
		if !strings.Contains(got, "partial output") {
			t.Fatalf("captureStderr dropped what the function wrote: got %q", got)
		}
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCaptureStderrReturnsWhenTheWriteEndStaysOpen$", "-test.timeout=15s")
	cmd.Env = append(os.Environ(), "CITE_CAPTURE_STDERR_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("captureStderr did not return while the write end was still open (child exited %v); it must not block the test binary:\n%s", err, out)
	}
}

// TestCaptureStderrRestoresStderrWhenFnPanics covers the other way out of the
// helper. A panic inside the function under test is how a real failure first
// reaches captureStderr. The helper has to put os.Stderr back while the panic
// unwinds: a binary left writing into a pipe with no live reader loses the
// output of every test that runs afterwards.
func TestCaptureStderrRestoresStderrWhenFnPanics(t *testing.T) {
	saved := os.Stderr
	held := saved
	t.Cleanup(func() {
		// Left dangling by a failing helper, this stream is a pipe with no
		// reader left; hand the binary back its stderr and drop it.
		os.Stderr = saved
		if held != saved {
			held.Close()
		}
	})

	var during *os.File
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the panic did not propagate out of captureStderr; a swallowed panic would hide the failure under test")
			}
		}()
		captureStderr(t, func() {
			during = os.Stderr
			panic("the function under test failed")
		})
	}()

	if os.Stderr != saved {
		held = os.Stderr
		t.Fatalf("os.Stderr = %s after captureStderr unwound from a panic, want %s; every later test writes into a pipe nobody reads",
			os.Stderr.Name(), saved.Name())
	}
	// The redirection has to have been in force for the duration of the call,
	// and gone by the time the panic reached us. `during` is read inside the
	// call, so it must differ from the process's real stderr; it must not be
	// compared against os.Stderr here, which has already been restored.
	if during == nil || during == saved {
		name := "<nil>"
		if during != nil {
			name = during.Name()
		}
		t.Errorf("os.Stderr was %s while the function under test ran, want the capture pipe; captureStderr captured nothing",
			name)
	}
}
