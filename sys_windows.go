//go:build windows
// +build windows

package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unsafe"
)

const (
	fileFlagBackupSemantics = 0x02000000
	fileFlagOpenReparse     = 0x00200000
	fileWriteAttributes     = 0x00000100
	fileShareRead           = 0x00000001
	fileShareWrite          = 0x00000002
	fileShareDelete         = 0x00000004
	fsctlSetReparsePoint    = 0x000900A4
	reparseTagMountPoint    = 0xA0000003
)

var (
	kernel32            = syscall.MustLoadDLL("kernel32.dll")
	procSetStdHandle    = kernel32.MustFindProc("SetStdHandle")
	procCreateFileW     = kernel32.MustFindProc("CreateFileW")
	procDeviceIoControl = kernel32.MustFindProc("DeviceIoControl")
	procCloseHandle     = kernel32.MustFindProc("CloseHandle")
	trackIDOnlyRe       = regexp.MustCompile(`^[1-9][0-9]*:$`)
)

type windowsLegacyEac3toArgs struct {
	args          []string
	staged        []stagedOutput
	workspace     string
	workspaceRoot string
	aliases       map[string]string
	sourceLinks   []string
}

type stagedOutput struct {
	stagedPath string
	finalPath  string
}

// prepareLegacyEac3toArgs makes every path handed to legacy eac3to ASCII-only.
// It only understands the canonical TID: FILENAME form produced by
// parseEac3toArgs and leaves unrecognized options untouched.
func prepareLegacyEac3toArgs(args []string) (*legacyEac3toArgs, error) {
	legacy := &windowsLegacyEac3toArgs{
		args:    append([]string(nil), args...),
		aliases: make(map[string]string),
	}

	for i := 0; i < len(legacy.args); i++ {
		arg := legacy.args[i]
		if trackIDOnlyRe.MatchString(arg) && i+1 < len(legacy.args) {
			output, err := legacy.prepareOutput(legacy.args[i+1])
			if err != nil {
				legacy.cleanup()
				return nil, err
			}
			legacy.args[i+1] = output
			i++
			continue
		}
		input, err := legacy.prepareInput(arg)
		if err != nil {
			legacy.cleanup()
			return nil, err
		}
		legacy.args[i] = input
	}
	return &legacyEac3toArgs{
		args:     legacy.args,
		finalize: legacy.finalize,
		cleanup:  legacy.cleanup,
	}, nil
}

func (legacy *windowsLegacyEac3toArgs) prepareInput(path string) (string, error) {
	if !filepath.IsAbs(path) || !hasNonASCII(path) || !pathExists(path) {
		return path, nil
	}
	return legacy.aliasExistingPath(path)
}

func (legacy *windowsLegacyEac3toArgs) prepareOutput(path string) (string, error) {
	if !filepath.IsAbs(path) || !hasNonASCII(path) {
		return path, nil
	}

	dir, leaf := filepath.Split(path)
	dir = strings.TrimRight(dir, `\\/`)
	if dir == "" || !pathExists(dir) {
		return "", fmt.Errorf("output directory does not exist: %q", filepath.Dir(path))
	}
	alias, err := legacy.aliasDirectory(dir)
	if err != nil {
		return "", err
	}
	if !hasNonASCII(leaf) {
		mapped := filepath.Join(alias, leaf)
		log.Printf("mapped legacy eac3to output %q to %q", path, mapped)
		return mapped, nil
	}

	ext := filepath.Ext(leaf)
	if hasNonASCII(ext) {
		return "", fmt.Errorf("output extension must be ASCII for legacy eac3to: %q", path)
	}
	staged, err := legacy.newStagedOutput(alias, ext)
	if err != nil {
		return "", err
	}
	legacy.staged = append(legacy.staged, stagedOutput{stagedPath: staged, finalPath: path})
	log.Printf("staged legacy eac3to output %q at %q", path, staged)
	return staged, nil
}

func (legacy *windowsLegacyEac3toArgs) aliasExistingPath(path string) (string, error) {
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return legacy.aliasDirectory(clean)
	}

	dir, leaf := filepath.Split(clean)
	alias, err := legacy.aliasDirectory(strings.TrimRight(dir, `\\/`))
	if err != nil {
		return "", err
	}
	if !hasNonASCII(leaf) {
		mapped := filepath.Join(alias, leaf)
		log.Printf("mapped legacy eac3to input %q to %q", path, mapped)
		return mapped, nil
	}

	ext := filepath.Ext(leaf)
	if hasNonASCII(ext) {
		return "", fmt.Errorf("source extension must be ASCII for legacy eac3to: %q", path)
	}
	link, err := legacy.newSourceLink(clean, dir, ext)
	if err != nil {
		return "", err
	}
	mapped := filepath.Join(alias, filepath.Base(link))
	log.Printf("mapped legacy eac3to input %q to %q", path, mapped)
	return mapped, nil
}

func (legacy *windowsLegacyEac3toArgs) ensureWorkspace() error {
	if legacy.workspace != "" {
		return nil
	}
	root, err := asciiWorkspaceRoot()
	if err != nil {
		return err
	}
	workspace, err := os.MkdirTemp(root, "eac3to-wrapper-")
	if err != nil {
		return fmt.Errorf("create alias workspace under %q: %w", root, err)
	}
	if hasNonASCII(workspace) {
		_ = os.RemoveAll(workspace)
		return fmt.Errorf("alias workspace is not ASCII-only: %q", workspace)
	}
	legacy.workspace = workspace
	legacy.workspaceRoot = root
	return nil
}

func asciiWorkspaceRoot() (string, error) {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Dir(exe))
	}
	candidates = append(candidates, os.TempDir())

	for _, root := range candidates {
		root = filepath.Clean(root)
		if hasNonASCII(root) || !filepath.IsAbs(root) || !pathExists(root) {
			continue
		}
		probe, err := os.CreateTemp(root, ".eac3to-wrapper-probe-")
		if err != nil {
			continue
		}
		name := probe.Name()
		_ = probe.Close()
		_ = os.Remove(name)
		return root, nil
	}
	return "", fmt.Errorf("wrapper directory and temporary directory must provide a writable ASCII-only alias workspace")
}

func (legacy *windowsLegacyEac3toArgs) aliasDirectory(dir string) (string, error) {
	dir = filepath.Clean(dir)
	key := strings.ToLower(dir)
	if alias, ok := legacy.aliases[key]; ok {
		return alias, nil
	}
	if err := legacy.ensureWorkspace(); err != nil {
		return "", err
	}

	alias := filepath.Join(legacy.workspace, fmt.Sprintf("p%d", len(legacy.aliases)))
	if err := makeJunction(alias, dir); err != nil {
		return "", err
	}
	legacy.aliases[key] = alias
	log.Printf("mapped legacy eac3to directory %q to %q", dir, alias)
	return alias, nil
}

func (legacy *windowsLegacyEac3toArgs) newSourceLink(source, dir, ext string) (string, error) {
	file, err := os.CreateTemp(dir, "eac3to-wrapper-input-*")
	if err != nil {
		return "", err
	}
	link := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(link); err != nil {
		return "", err
	}
	link += ext
	if err := os.Link(source, link); err != nil {
		return "", fmt.Errorf("create ASCII source hard link for %q: %w", source, err)
	}
	legacy.sourceLinks = append(legacy.sourceLinks, link)
	return link, nil
}

func (legacy *windowsLegacyEac3toArgs) newStagedOutput(dir, ext string) (string, error) {
	file, err := os.CreateTemp(dir, "eac3to-wrapper-output-*")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path + ext, nil
}

func (legacy *windowsLegacyEac3toArgs) finalize() error {
	for _, output := range legacy.staged {
		if pathExists(output.finalPath) {
			return fmt.Errorf("output already exists; staged output retained at %q: %q", output.stagedPath, output.finalPath)
		}
		if err := os.Rename(output.stagedPath, output.finalPath); err != nil {
			return fmt.Errorf("move staged output %q to %q (staged output retained): %w", output.stagedPath, output.finalPath, err)
		}
		log.Printf("moved staged eac3to output %q to %q", output.stagedPath, output.finalPath)
	}
	return nil
}

func (legacy *windowsLegacyEac3toArgs) cleanup() {
	for _, link := range legacy.sourceLinks {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			log.Printf("unable to remove source hard link %q: %v", link, err)
		}
	}
	if legacy.workspace != "" {
		if err := os.RemoveAll(legacy.workspace); err != nil {
			log.Printf("unable to remove alias workspace %q: %v", legacy.workspace, err)
		}
	}
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

// makeJunction creates a mount-point reparse point with Unicode Windows APIs.
// It intentionally avoids cmd.exe and its separate argument/code-page parsing.
func makeJunction(link, target string) error {
	if strings.HasPrefix(target, `\\`) {
		return fmt.Errorf("UNC paths cannot be used as directory junction targets: %q", target)
	}
	if err := os.Mkdir(link, 0755); err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(link) }

	substitute := `\??\` + target
	printName := target
	substitute16 := syscall.StringToUTF16(substitute)
	print16 := syscall.StringToUTF16(printName)
	// REPARSE_DATA_BUFFER header (8 bytes), mount-point header (8 bytes), then
	// substitute and print names with terminating UTF-16 NULs.
	pathBytes := (len(substitute16) + len(print16)) * 2
	buffer := make([]byte, 16+pathBytes)
	binary.LittleEndian.PutUint32(buffer[0:], reparseTagMountPoint)
	binary.LittleEndian.PutUint16(buffer[4:], uint16(8+pathBytes))
	substituteBytes := (len(substitute16) - 1) * 2
	printOffset := substituteBytes + 2
	printBytes := (len(print16) - 1) * 2
	binary.LittleEndian.PutUint16(buffer[8:], 0)
	binary.LittleEndian.PutUint16(buffer[10:], uint16(substituteBytes))
	binary.LittleEndian.PutUint16(buffer[12:], uint16(printOffset))
	binary.LittleEndian.PutUint16(buffer[14:], uint16(printBytes))
	for i, ch := range substitute16 {
		binary.LittleEndian.PutUint16(buffer[16+i*2:], ch)
	}
	printStart := 16 + len(substitute16)*2
	for i, ch := range print16 {
		binary.LittleEndian.PutUint16(buffer[printStart+i*2:], ch)
	}

	link16, err := syscall.UTF16PtrFromString(link)
	if err != nil {
		cleanup()
		return err
	}
	handle, _, callErr := procCreateFileW.Call(
		uintptr(unsafe.Pointer(link16)), fileWriteAttributes,
		fileShareRead|fileShareWrite|fileShareDelete, 0, syscall.OPEN_EXISTING,
		fileFlagOpenReparse|fileFlagBackupSemantics, 0,
	)
	if handle == uintptr(syscall.InvalidHandle) {
		cleanup()
		return fmt.Errorf("open junction %q: %v", link, callErr)
	}
	defer procCloseHandle.Call(handle)

	var bytesReturned uint32
	ok, _, callErr := procDeviceIoControl.Call(
		handle, fsctlSetReparsePoint, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)),
		0, 0, uintptr(unsafe.Pointer(&bytesReturned)), 0,
	)
	if ok == 0 {
		cleanup()
		return fmt.Errorf("set junction target %q: %v", target, callErr)
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
