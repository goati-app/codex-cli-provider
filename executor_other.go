//go:build !unix

package codexcli

import (
	"context"
)

// Executor is unavailable on non-Unix systems because descendant process-group
// termination is part of the public safety contract.
type Executor struct{}

func New(Config) (*Executor, error)  { return nil, newError(UnsupportedCapability, "platform", nil) }
func (*Executor) IsConfigured() bool { return false }
func (*Executor) Run(context.Context, Request) (Result, error) {
	return Result{}, newError(UnsupportedCapability, "platform", nil)
}
