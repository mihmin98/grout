package archive

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"go.uber.org/atomic"
)

// sevenZipBinary is the standalone 7-Zip the firmware may ship. It is looked
// up rather than bundled, so extracting with it is only offered where it is.
const sevenZipBinary = "7za"

// Exit codes 7-Zip documents.
const (
	sevenZipWarning      = 1
	sevenZipCommandLine  = 7
	sevenZipOutOfMemory  = 8
	sevenZipNiceness     = 10
	stderrTail           = 4 * 1024
	progressTokenMaxSize = 256
)

// ErrOutOfMemory is 7-Zip running out of memory while unpacking, which on a
// device with little RAM means the archive's dictionary is too large for it.
var ErrOutOfMemory = errors.New("7-Zip ran out of memory")

// Entry is one item in an archive's listing.
type Entry struct {
	// Name is the entry's path inside the archive, with forward slashes.
	Name  string
	Size  uint64
	IsDir bool
	// Method is how the entry is compressed, as 7-Zip names it: "LZMA2:24",
	// "Deflate", "PPMD:o6:mem16m". Empty when the listing does not say.
	Method    string
	Encrypted bool
}

// SevenZipPath finds the 7za binary, or reports that there is none.
func SevenZipPath() (string, bool) {
	path, err := exec.LookPath(sevenZipBinary)
	return path, err == nil
}

// SevenZipList lists an archive with 7za. Listing with the same tool that
// unpacks means a format it can read is never refused by a weaker reader.
func SevenZipList(ctx context.Context, bin, archivePath string) ([]Entry, error) {
	cmd := exec.CommandContext(ctx, bin, "l", "-slt", "--", archivePath)
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting 7za: %w", err)
	}

	entries, parseErr := parseSevenZipListing(stdout)
	// Drain whatever the parser left so 7za is not blocked writing.
	_, _ = io.Copy(io.Discard, stdout)

	if err := cmd.Wait(); err != nil {
		return nil, sevenZipFailure(ctx, err, stderr.String())
	}
	if parseErr != nil {
		return nil, fmt.Errorf("reading the 7za listing: %w", parseErr)
	}
	return entries, nil
}

// SevenZipExtract unpacks an archive into destDir with 7za, reporting progress
// as it goes. Cancelling ctx stops 7za.
//
// 7za is asked to decode on one thread, which keeps its memory to about one
// dictionary on firmwares with little RAM. An older 7za that does not know the
// switch rejects the command line, and is run again without it.
func SevenZipExtract(ctx context.Context, bin, archivePath, destDir string, progress *atomic.Float64) error {
	err := runSevenZipExtract(ctx, bin, archivePath, destDir, progress, true)
	var failure *SevenZipError
	if errors.As(err, &failure) && failure.Code == sevenZipCommandLine {
		return runSevenZipExtract(ctx, bin, archivePath, destDir, progress, false)
	}
	return err
}

func runSevenZipExtract(ctx context.Context, bin, archivePath, destDir string, progress *atomic.Float64, singleThread bool) error {
	// -y and -aoa answer every question 7za could ask, and stdin is left
	// unset, so it can never sit waiting for input no one will give. -p with
	// no password fails an encrypted entry rather than prompting for one.
	args := []string{"x", "-y", "-aoa", "-p", "-bb0", "-bso0", "-bsp1", "-o" + destDir}
	if singleThread {
		args = append(args, "-mmt1")
	}
	args = append(args, "--", archivePath)

	cmd := exec.CommandContext(ctx, bin, args...)
	stderr := &tailBuffer{max: stderrTail}
	cmd.Stderr = stderr
	cmd.Stdout = &progressParser{progress: progress}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting 7za: %w", err)
	}
	// Writing hundreds of megabytes to a slow card should not starve the
	// screen showing its progress. Failing to lower it changes nothing else.
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, sevenZipNiceness)

	if err := cmd.Wait(); err != nil {
		return sevenZipFailure(ctx, err, stderr.String())
	}
	if progress != nil {
		progress.Store(1)
	}
	return nil
}

// SevenZipError is 7za exiting with an error.
type SevenZipError struct {
	Code    int
	Message string
}

func (e *SevenZipError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("7za exited with code %d", e.Code)
	}
	return fmt.Sprintf("7za exited with code %d: %s", e.Code, e.Message)
}

func (e *SevenZipError) Is(target error) bool {
	return target == ErrOutOfMemory && e.Code == sevenZipOutOfMemory
}

// sevenZipFailure turns how 7za ended into an error, or nil for an exit that
// only warned. A warning (a timestamp or permission the card would not take)
// leaves the files written; the caller still finds out if one is missing.
func sevenZipFailure(ctx context.Context, err error, stderr string) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Errorf("running 7za: %w", err)
	}
	if exitErr.ExitCode() == sevenZipWarning {
		return nil
	}
	return &SevenZipError{Code: exitErr.ExitCode(), Message: lastLines(stderr, 3)}
}

// parseSevenZipListing reads the technical listing (-slt): the archive's own
// properties, a line of dashes, then one block of "Key = Value" lines per
// entry, separated by blank lines.
func parseSevenZipListing(r io.Reader) ([]Entry, error) {
	var entries []Entry
	var current *Entry
	pastHeader := false

	flush := func() {
		if current != nil && current.Name != "" {
			entries = append(entries, *current)
		}
		current = nil
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if !pastHeader {
			pastHeader = strings.HasPrefix(line, "----------")
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}

		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			// "Key =" with nothing after it.
			key, ok = strings.CutSuffix(line, " =")
			if !ok {
				continue
			}
			value = ""
		}
		if current == nil {
			current = &Entry{}
		}

		switch key {
		case "Path":
			current.Name = value
		case "Size":
			current.Size, _ = strconv.ParseUint(value, 10, 64)
		case "Folder":
			current.IsDir = current.IsDir || value == "+"
		case "Attributes":
			// "D drwxr-xr-x" for a folder in a 7z, "A -rw-r--r--" for a file.
			current.IsDir = current.IsDir || strings.HasPrefix(value, "D")
		case "Method":
			current.Method = value
		case "Encrypted":
			current.Encrypted = value == "+"
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if !pastHeader {
		return nil, errors.New("no entries in the listing")
	}
	return entries, nil
}

// progressParser reads 7za's progress output (-bsp1), which redraws one line
// with backspaces: " 67% - Game.gbc\b\b\b...". Each redraw is parsed as it
// ends; nothing beyond the current one is kept.
type progressParser struct {
	progress *atomic.Float64
	token    []byte
}

func (p *progressParser) Write(b []byte) (int, error) {
	for _, c := range b {
		switch c {
		case '\b', '\r', '\n':
			p.flush()
		default:
			if len(p.token) < progressTokenMaxSize {
				p.token = append(p.token, c)
			}
		}
	}
	return len(b), nil
}

func (p *progressParser) flush() {
	if percent, ok := parsePercent(p.token); ok && p.progress != nil && percent/100 > p.progress.Load() {
		p.progress.Store(percent / 100)
	}
	p.token = p.token[:0]
}

// parsePercent finds the number before the first '%' in a progress line.
func parsePercent(token []byte) (float64, bool) {
	end := strings.IndexByte(string(token), '%')
	if end <= 0 {
		return 0, false
	}
	start := end
	for start > 0 && token[start-1] >= '0' && token[start-1] <= '9' {
		start--
	}
	if start == end {
		return 0, false
	}
	n, err := strconv.Atoi(string(token[start:end]))
	if err != nil || n > 100 {
		return 0, false
	}
	return float64(n), true
}

// tailBuffer keeps only the last max bytes written to it, so a chatty failure
// cannot grow without bound on a device with little memory.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.buf = append(t.buf, b...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }

// lastLines is the last n non-empty lines of s, joined with "; ".
func lastLines(s string, n int) string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
