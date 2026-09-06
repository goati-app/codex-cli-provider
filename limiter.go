package codexcli

import "context"

// Limiter is an in-process capacity pool. Share one Limiter among Executors
// when a consumer needs coordination across executor instances. It makes no
// promises across OS processes or containers.
type Limiter struct{ slots chan struct{} }

func NewLimiter(capacity int) *Limiter {
	if capacity <= 0 {
		return nil
	}
	return &Limiter{slots: make(chan struct{}, capacity)}
}

func (l *Limiter) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if l == nil {
		return func() {}, nil
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
