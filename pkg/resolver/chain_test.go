package resolver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dgr237/tflens/pkg/resolver"
)

// fakeResolver is a test double that returns a preset result and records how
// many times it was asked.
type fakeResolver struct {
	result *resolver.Resolved
	err    error
	calls  int
}

func (f *fakeResolver) Resolve(context.Context, resolver.Ref) (*resolver.Resolved, error) {
	f.calls++
	return f.result, f.err
}

func TestChainReturnsFirstSuccess(t *testing.T) {
	first := &fakeResolver{result: &resolver.Resolved{Dir: "/first", Kind: resolver.KindLocal}}
	second := &fakeResolver{result: &resolver.Resolved{Dir: "/second"}}
	chain := resolver.NewChain(first, second)

	got, err := chain.Resolve(context.Background(), resolver.Ref{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Dir != "/first" {
		t.Errorf("Dir = %q, want /first", got.Dir)
	}
	if second.calls != 0 {
		t.Errorf("second resolver should not be called on first success, got %d calls", second.calls)
	}
}

func TestChainFallsThroughOnErrNotApplicable(t *testing.T) {
	first := &fakeResolver{err: resolver.ErrNotApplicable}
	second := &fakeResolver{result: &resolver.Resolved{Dir: "/second", Kind: resolver.KindManifest}}
	chain := resolver.NewChain(first, second)

	got, err := chain.Resolve(context.Background(), resolver.Ref{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Dir != "/second" {
		t.Errorf("Dir = %q, want /second", got.Dir)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Errorf("expected both resolvers called once, got first=%d second=%d", first.calls, second.calls)
	}
}

func TestChainStopsOnHardError(t *testing.T) {
	boom := errors.New("boom")
	first := &fakeResolver{err: boom}
	second := &fakeResolver{result: &resolver.Resolved{Dir: "/second"}}
	chain := resolver.NewChain(first, second)

	_, err := chain.Resolve(context.Background(), resolver.Ref{})
	if !errors.Is(err, boom) {
		t.Errorf("hard error should propagate, got %v", err)
	}
	if second.calls != 0 {
		t.Errorf("second resolver should not be called after hard error, got %d calls", second.calls)
	}
}

func TestChainAllNotApplicableReturnsNotApplicable(t *testing.T) {
	first := &fakeResolver{err: resolver.ErrNotApplicable}
	second := &fakeResolver{err: resolver.ErrNotApplicable}
	chain := resolver.NewChain(first, second)

	_, err := chain.Resolve(context.Background(), resolver.Ref{})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("all not-applicable should return ErrNotApplicable, got %v", err)
	}
}

func TestChainEmptyChainReturnsNotApplicable(t *testing.T) {
	chain := resolver.NewChain()
	_, err := chain.Resolve(context.Background(), resolver.Ref{})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("empty chain should return ErrNotApplicable, got %v", err)
	}
}
