package initrd

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
)

// Builder creates per-job initrd images by appending a script to a base initrd.
type Builder struct {
	BaseInitrdPath string
}

func New(baseInitrdPath string) *Builder {
	return &Builder{BaseInitrdPath: baseInitrdPath}
}

// initScript is the /init replacement that runs the injected job script
// and powers off the VM when done.
const initScript = `#!/bin/sh
mount -t proc proc /proc
mount -t sysfs sysfs /sys
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true

if [ -x /job/script.sh ]; then
    echo "=== cerberOS job start ==="
    /bin/sh /job/script.sh 2>&1
    EXIT_CODE=$?
    echo "=== cerberOS job exit_code=$EXIT_CODE ==="
else
    echo "=== cerberOS VM ready ==="
    exec /bin/sh
fi

echo o > /proc/sysrq-trigger
`

// Build creates a new initrd.gz with the given script embedded at /job/script.sh
// and a replacement /init that executes it. Returns the path to a temp file;
// the caller must os.Remove it when done.
func (b *Builder) Build(script []byte) (string, error) {
	// Read and decompress the base initrd
	compressed, err := os.ReadFile(b.BaseInitrdPath)
	if err != nil {
		return "", fmt.Errorf("read base initrd: %w", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return "", fmt.Errorf("gunzip base initrd: %w", err)
	}
	base, err := io.ReadAll(gr)
	gr.Close()
	if err != nil {
		return "", fmt.Errorf("read base initrd cpio: %w", err)
	}

	// Strip the TRAILER!!! entry so we can append new entries before it.
	// The trailer header starts with "070701" and contains "TRAILER!!!".
	trailerIdx := findTrailer(base)
	if trailerIdx < 0 {
		return "", fmt.Errorf("TRAILER!!! not found in base initrd")
	}
	base = base[:trailerIdx]

	// Build new cpio with appended entries
	var buf bytes.Buffer
	buf.Write(base)

	inode := uint32(900000) // high inode to avoid collisions with base entries

	// /job directory
	writeCPIOEntry(&buf, "job", 040755, nil, inode)
	inode++

	// /job/script.sh (executable)
	writeCPIOEntry(&buf, "job/script.sh", 0100755, script, inode)
	inode++

	// /init replacement (overwrites the base /init because later cpio entries win)
	writeCPIOEntry(&buf, "init", 0100755, []byte(initScript), inode)
	inode++

	// Re-append trailer
	writeCPIOEntry(&buf, "TRAILER!!!", 0, nil, 0)

	// Gzip and write to temp file
	tmp, err := os.CreateTemp("", "cerberOS-initrd-*.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}

	gw := gzip.NewWriter(tmp)
	if _, err := gw.Write(buf.Bytes()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("gzip write: %w", err)
	}
	if err := gw.Close(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("gzip close: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("close temp file: %w", err)
	}

	return tmp.Name(), nil
}

// findTrailer locates the byte offset of the TRAILER!!! cpio header in the archive.
func findTrailer(data []byte) int {
	marker := []byte("TRAILER!!!")
	idx := bytes.LastIndex(data, marker)
	if idx < 0 {
		return -1
	}
	// Walk backwards to find the "070701" magic that starts this entry's header.
	// The header is 110 bytes and the filename follows immediately after.
	// Search backwards from the TRAILER!!! position for the magic number.
	magic := []byte("070701")
	for i := idx - 1; i >= 0 && i >= idx-120; i-- {
		if bytes.HasPrefix(data[i:], magic) {
			return i
		}
	}
	return -1
}

// writeCPIOEntry writes a single newc-format cpio entry.
// newc format: 110-byte ASCII hex header, null-terminated filename, data.
// Both filename and data are padded to 4-byte alignment.
func writeCPIOEntry(w *bytes.Buffer, name string, mode uint32, data []byte, ino uint32) {
	nameWithNull := name + "\x00"
	nameSize := len(nameWithNull)
	dataSize := len(data)
	nlink := uint32(1)
	if mode&040000 != 0 { // directory
		nlink = 2
	}

	hdr := fmt.Sprintf("070701"+
		"%08X"+ // inode
		"%08X"+ // mode
"%08X"+ // uid
		"%08X"+ // gid
		"%08X"+ // nlink
		"%08X"+ // mtime
		"%08X"+ // filesize
		"%08X"+ // devmajor
		"%08X"+ // devminor
		"%08X"+ // rdevmajor
		"%08X"+ // rdevminor
		"%08X"+ // namesize
		"%08X", // checksum
		ino, mode, 0, 0, nlink, 0, dataSize, 0, 0, 0, 0, nameSize, 0,
	)
	w.WriteString(hdr)
	w.WriteString(nameWithNull)

	// Pad name to 4-byte boundary (header is 110 bytes + nameSize, pad total)
	namePad := (4 - (110+nameSize)%4) % 4
	for i := 0; i < namePad; i++ {
		w.WriteByte(0)
	}

	if dataSize > 0 {
		w.Write(data)
		dataPad := (4 - dataSize%4) % 4
		for i := 0; i < dataPad; i++ {
			w.WriteByte(0)
		}
	}
}
