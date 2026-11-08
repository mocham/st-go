// Package limits owns resource budgets shared by the parser and frontends.
package limits

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"time"
)

// MaxBytes is the common upper bound for one input, decoded bitmap, or buffer.
// Other budgets below are derived from it. These are per-resource limits,
// not a process-wide RSS limit (native decoders also use working memory).
const (
	MaxBytes             = 64 << 20
	FileBytes            = MaxBytes
	PixelBytes           = MaxBytes
	ControlBytes         = MaxBytes / 64
	ClipboardBytes       = MaxBytes / 8
	GlyphCacheBytes      = MaxBytes / 2
	GlyphCacheEntries    = MaxBytes / 4096
	PreviewCacheBytes    = MaxBytes * 4 // 256 MiB per continuous-PDF strip
	PreviewScreens       = 5
	PreviewFrameInterval = 16 * time.Millisecond
	PreviewPages         = MaxBytes / 4096
	PreviewHeight        = MaxBytes // total continuous-document height in pixels
	PreviewSearchBytes   = MaxBytes / 65536
	AnimationFrames      = MaxBytes / 1024
	LogBytes             = MaxBytes / 64
)

var diagnostics struct {
	sync.Mutex
	file *os.File
	size int
}

// Report writes bounded diagnostic records to a private, per-process file in
// /tmp. The file is created lazily; wrap it in place to bound disk usage too.
func Report(scope, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	if len(message) > 1024 {
		message = message[:1024] + "..."
	}
	line := fmt.Sprintf("%s %s: %s\n", time.Now().Format(time.RFC3339), scope, message)
	diagnostics.Lock()
	defer diagnostics.Unlock()
	if diagnostics.file == nil {
		f, err := os.CreateTemp("/tmp", fmt.Sprintf("st-go-%d-errors-*.log", os.Getpid()))
		if err != nil {
			log.Printf("resource limit: %s (cannot create diagnostic log: %v)", message, err)
			return
		}
		diagnostics.file = f
		log.Printf("resource-limit errors: %s", f.Name())
	}
	if diagnostics.size+len(line) > LogBytes {
		if err := diagnostics.file.Truncate(0); err != nil {
			return
		}
		if _, err := diagnostics.file.Seek(0, io.SeekStart); err != nil {
			return
		}
		diagnostics.size = 0
	}
	n, err := io.WriteString(diagnostics.file, line)
	diagnostics.size += n
	if err != nil {
		log.Printf("resource-limit log: %v", err)
	}
}

func Check(scope string, size int64, maximum int) bool {
	if size < 0 || size > int64(maximum) {
		Report(scope, "rejected size %d; limit %d bytes/items", size, maximum)
		return false
	}
	return true
}

// Product checks dimensions before multiplication, including integer overflow.
func Product(scope string, maximum int, dimensions ...int) (int, bool) {
	n := 1
	for _, d := range dimensions {
		if d <= 0 || d > maximum/n {
			Report(scope, "rejected dimensions %v; limit %d bytes/items", dimensions, maximum)
			return 0, false
		}
		n *= d
	}
	return n, true
}

func RGBA(scope string, width, height int) (int, bool) {
	return Product(scope, PixelBytes, width, height, 4)
}

func Grid(cols, rows int) bool {
	// Two 16-byte glyph screens plus tab/damage bookkeeping headroom.
	_, ok := Product("terminal grid", MaxBytes, cols, rows, 40)
	return ok
}

// ReadFile rejects oversized inputs before allocation and also bounds reads
// if a regular file grows after Stat. Devices/FIFOs are not preview inputs.
func ReadFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		Report("preview", "rejected non-regular file %q", path)
		return nil, fmt.Errorf("preview requires a regular file")
	}
	if !Check("preview file", info.Size(), FileBytes) {
		return nil, fmt.Errorf("preview exceeds %d bytes", FileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, FileBytes+1))
	if !Check("preview file", int64(len(data)), FileBytes) {
		return nil, fmt.Errorf("preview exceeds %d bytes", FileBytes)
	}
	return data, err
}

// Buffer bounds encoded output while it is being produced, rather than after
// an encoder has already retained an arbitrarily large result.
type Buffer struct {
	bytes.Buffer
	Scope string
}

func (b *Buffer) Write(p []byte) (int, error) {
	if !Check(b.Scope, int64(b.Len())+int64(len(p)), FileBytes) {
		return 0, fmt.Errorf("encoded output exceeds %d bytes", FileBytes)
	}
	return b.Buffer.Write(p)
}
