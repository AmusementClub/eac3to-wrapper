//go:build linux || darwin
// +build linux darwin

package main

import (
	"log"
	"os"
	"syscall"
)

func prepareLegacyEac3toArgs(args []string) (*legacyEac3toArgs, error) {
	return &legacyEac3toArgs{
		args:     args,
		finalize: func() error { return nil },
		cleanup:  func() {},
	}, nil
}

// redirectStderr redirects all stderr output (specifically, panic) to given f.
// see https://stackoverflow.com/a/34773942.
func redirectStderr(f *os.File) {
	err := syscall.Dup2(int(f.Fd()), int(os.Stderr.Fd()))
	if err != nil {
		log.Fatalf("failed to redirect stderr to file: %v", err)
	}
}
