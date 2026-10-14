//go:build !unix

package installmedia

import (
	"context"
	"errors"
)

var errNoCommands = errors.New("command sources need a unix host")

func runCommand(ctx context.Context, argv []string, dir string, env []string, o Options) error {
	return errNoCommands
}

func makeReadable(path string) error { return errNoCommands }
