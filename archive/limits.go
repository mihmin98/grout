package archive

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// FreeBytes is how much space an unprivileged write to dir can still use, or
// false when the filesystem will not say.
func FreeBytes(dir string) (uint64, bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, false
	}
	return uint64(stat.Bavail) * uint64(stat.Bsize), true
}

// AvailableMemory is the kernel's estimate of memory a new process can use
// without swapping (MemAvailable), or false where there is no /proc/meminfo.
func AvailableMemory() (uint64, bool) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		rest, ok := strings.CutPrefix(scanner.Text(), "MemAvailable:")
		if !ok {
			continue
		}
		kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
		if err != nil {
			return 0, false
		}
		return kb * 1024, true
	}
	return 0, false
}

// DecoderMemory estimates the memory unpacking with a method takes, from
// 7-Zip's name for it: the dictionary of LZMA/LZMA2, the model of PPMd. Other
// methods (Deflate, BZip2, Store, filters) need little and count as zero, as
// does a name that cannot be read.
//
// A bare number is a power of two ("LZMA:24" is 16 MiB, "mem24" likewise); a
// suffixed one is a size ("LZMA2:64m"). A chain ("BCJ2 LZMA2:24 LZMA:20")
// takes its largest.
func DecoderMemory(method string) uint64 {
	var largest uint64
	for _, coder := range strings.Fields(method) {
		name, params, _ := strings.Cut(coder, ":")
		var size uint64
		switch strings.ToUpper(name) {
		case "LZMA", "LZMA2":
			// The dictionary is the first parameter.
			first, _, _ := strings.Cut(params, ":")
			size = parseCoderSize(first)
		case "PPMD":
			for _, param := range strings.Split(params, ":") {
				if mem, ok := strings.CutPrefix(param, "mem"); ok {
					size = parseCoderSize(mem)
				}
			}
		}
		largest = max(largest, size)
	}
	return largest
}

// parseCoderSize reads "24" as 2^24 and "3m"/"512k"/"1g"/"1536m" as sizes.
func parseCoderSize(s string) uint64 {
	if s == "" {
		return 0
	}
	multiplier := uint64(0)
	switch s[len(s)-1] {
	case 'b', 'B':
		multiplier = 1
	case 'k', 'K':
		multiplier = 1 << 10
	case 'm', 'M':
		multiplier = 1 << 20
	case 'g', 'G':
		multiplier = 1 << 30
	}
	if multiplier == 0 {
		exp, err := strconv.ParseUint(s, 10, 8)
		if err != nil || exp > 40 {
			return 0
		}
		return 1 << exp
	}
	n, err := strconv.ParseUint(s[:len(s)-1], 10, 64)
	if err != nil {
		return 0
	}
	return n * multiplier
}
