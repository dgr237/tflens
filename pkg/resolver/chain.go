package resolver

import (
	"context"
	"errors"
)

// Chain tries each inner resolver in order, returning the first successful
// resolution. ErrNotApplicable falls through to the next resolver; any other
// error stops the chain and is returned verbatim.
//
// If every resolver returns ErrNotApplicable, Chain itself returns
// ErrNotApplicable, so callers can distinguish "nobody claimed this ref"
// from "somebody tried and failed".
type Chain struct {
	resolvers []Resolver
}

func NewChain(resolvers ...Resolver) *Chain {
	return &Chain{resolvers: resolvers}
}

func (c *Chain) Resolve(ctx context.Context, ref Ref) (*Resolved, error) {
	for _, r := range c.resolvers {
		res, err := r.Resolve(ctx, ref)
		if err == nil {
			return res, nil
		}
		if !errors.Is(err, ErrNotApplicable) {
			return nil, err
		}
	}
	return nil, ErrNotApplicable
}
