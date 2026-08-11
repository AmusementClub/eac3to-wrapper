//go:build windows
// +build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

var (
	kernel32         = syscall.MustLoadDLL("kernel32.dll")
	procSetStdHandle = kernel32.MustFindProc("SetStdHandle")
)

// repairEac3toArgs creates ASCII directory junctions for existing absolute
// paths containing non-ASCII characters. Legacy eac3to sees only the alias,
// while Go creates and deletes the junction through Unicode-aware Windows APIs.
func repairEac3toArgs(args []string) ([]string, func(), error) {
	fixed := append([]string(nil), args...)
	workspace, err := os.MkdirTemp("", "eac3to-wrapper-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	alias := 0

	for i, arg := range fixed {
		if !filepath.IsAbs(arg) || !hasNonASCII(arg) || !pathExists(arg) {
			continue
		}
		dir, leaf := filepath.Split(arg)
		if leaf == "" {
			dir, leaf = filepath.Split(filepath.Clean(arg))
		}
		link := filepath.Join(workspace, fmt.Sprintf("p%d", alias))
		alias++
		if err := makeJunction(link, strings.TrimRight(dir, `\\/`)); err != nil {
			cleanup()
			return nil, nil, err
		}
		fixed[i] = filepath.Join(link, leaf)
		log.Printf("mapped legacy eac3to path %q to %q", arg, fixed[i])
	}
	return fixed, cleanup, nil
}

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > 0x7f {
			return true
		}
	}
	return false
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func makeJunction(link, target string) error {
	cmd := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create ASCII path alias for %q: %v: %s", target, err, out)
	}
	return nil
}

func setStdHandle(stdhandle int32, handle syscall.Handle) error {
	r0, _, e1 := syscall.Syscall(procSetStdHandle.Addr(), 2, uintptr(stdhandle), uintptr(handle), 0)
	if r0 == 0 {
		if e1 != 0 {
			return error(e1)
		}
		return syscall.EINVAL
	}
	return nil
}

// redirectStderr redirects all stderr output (specifically, panic) to given f.
// see https://stackoverflow.com/a/34773942.
func redirectStderr(f *os.File) {
	err := setStdHandle(syscall.STD_ERROR_HANDLE, syscall.Handle(f.Fd()))
	if err != nil {
		log.Fatalf("failed to redirect stderr to file: %v", err)
	}
	// SetStdHandle does not affect prior references to stderr
	os.Stderr = f
}
