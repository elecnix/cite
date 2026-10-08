package model

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// FailoverClient serves each call from the first leg that answers it: the
// primary model, then the fallback chain in order (docs/configuration.md,
// "Fallback"). Before it existed the chain was configured, documented and
// exercised by the canary, and no review call ever walked it.
//
// A call moves to the next leg only on a failure the next model may not
// share: a runaway generation (measured, one model ran away on the same file
// at every reasoning effort while three others answered it in two minutes),
// a status the endpoint will answer again for this model (retired, unknown,
// refused), an output overflow, or a provider that is down or limiting.
// A deadline is returned as is, because the next leg would inherit the same
// expired context, and so is a canceled run.
type FailoverClient struct {
	Legs []Client
	// Logf, when set, is told about every failover, so the run log says
	// which model served a call that the primary could not.
	Logf func(format string, args ...any)
}

// NewFailoverClient returns primary alone when there is no fallback, so a
// run without a chain behaves exactly as before.
func NewFailoverClient(primary Client, fallback []Client, logf func(string, ...any)) Client {
	if len(fallback) == 0 {
		return primary
	}
	return &FailoverClient{Legs: append([]Client{primary}, fallback...), Logf: logf}
}

// ModelID is the primary's: it is the model the run is configured for.
func (f *FailoverClient) ModelID() string { return f.Legs[0].ModelID() }

// Complete implements Client.
func (f *FailoverClient) Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error) {
	var lastErr error
	for i, leg := range f.Legs {
		resp, err := leg.Complete(ctx, req)
		if err == nil {
			if resp != nil && resp.Model == "" {
				resp.Model = leg.ModelID()
			}
			if i > 0 && f.Logf != nil {
				f.Logf("fallback model %s served the call the models before it could not", leg.ModelID())
			}
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil || !FailsOver(err) {
			return nil, err
		}
		if i+1 < len(f.Legs) && f.Logf != nil {
			f.Logf("model %s failed (%v); failing over to %s", leg.ModelID(), err, f.Legs[i+1].ModelID())
		}
	}
	return nil, fmt.Errorf("every model in the fallback chain failed; last: %w", lastErr)
}

// FailsOver reports whether err is a failure another model may not share.
func FailsOver(err error) bool {
	if errors.Is(err, ErrDeadline) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, ErrRunaway) || errors.Is(err, ErrDeterministic) {
		return true
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return true // unreachable: the next leg may be another endpoint
	}
	var te *typedError
	if errors.As(err, &te) {
		switch te.Code {
		case "rate_limited", "provider_unavailable", "provider_error":
			return true
		}
	}
	return false
}
