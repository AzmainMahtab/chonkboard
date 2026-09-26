// Package filestore keeps attachment bytes on disk, addressed by a generated name.
//
// Attachments do not go in the database. A 10MB blob in a SQLite row makes every
// query that touches the table drag it through memory, makes `VACUUM INTO` backups
// proportional to total upload size rather than to the board, and gives up the one
// thing a file on disk gets for free: being served by a syscall rather than through
// a driver.
package filestore

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrTooLarge means the upload exceeded the configured ceiling.
var ErrTooLarge = errors.New("filestore: upload is too large")

// ErrNotFound means there is no file under that name.
var ErrNotFound = errors.New("filestore: no such file")

// Disk stores files under one directory.
type Disk struct {
	root string
	// fanout splits files into 256 subdirectories by the first byte of their
	// name. A single directory with tens of thousands of entries is slow to list
	// and, on some filesystems, slow to open a file in.
	fanout bool
}

// NewDisk returns a store rooted at dir, creating it if it is missing.
func NewDisk(dir string) (*Disk, error) {
	if dir == "" {
		return nil, errors.New("filestore: upload directory is required")
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("filestore: create %s: %w", dir, err)
	}
	return &Disk{root: dir, fanout: true}, nil
}

// NewName returns a fresh storage name.
//
// Random, and never derived from what the uploader called the file. A user-supplied
// filename reaching the filesystem is a path-traversal bug waiting to happen
// (`../../etc/passwd`), a collision between two people uploading `report.pdf`, and on
// a case-insensitive filesystem a way to overwrite somebody else's upload. The
// original name is kept in the database and used only in a `Content-Disposition`
// header.
func NewName() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("filestore: generate name: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Save writes r under name, refusing anything past maxBytes.
//
// The limit is enforced while copying rather than by trusting a Content-Length: a
// request can lie about its length, and http.MaxBytesReader alone would abort the
// connection rather than let us return a readable message. Reading one byte past the
// ceiling is how we tell "exactly at the limit" from "over it".
func (d *Disk) Save(name string, r io.Reader, maxBytes int64) (int64, error) {
	path, err := d.pathFor(name)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, fmt.Errorf("filestore: create directory: %w", err)
	}

	// O_EXCL so a generated name can never silently overwrite an existing file.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return 0, fmt.Errorf("filestore: create %s: %w", name, err)
	}

	written, copyErr := io.Copy(f, io.LimitReader(r, maxBytes+1))
	closeErr := f.Close()

	switch {
	case copyErr != nil:
		_ = os.Remove(path)
		return 0, fmt.Errorf("filestore: write %s: %w", name, copyErr)
	case closeErr != nil:
		_ = os.Remove(path)
		return 0, fmt.Errorf("filestore: close %s: %w", name, closeErr)
	case written > maxBytes:
		// A partial file is worse than none: nothing references it, and it
		// would sit there until somebody audited the directory.
		_ = os.Remove(path)
		return 0, ErrTooLarge
	}
	return written, nil
}

// Open returns the file for reading. The caller closes it.
func (d *Disk) Open(name string) (*os.File, error) {
	path, err := d.pathFor(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("filestore: open %s: %w", name, err)
	}
	return f, nil
}

// Remove deletes a file. A file that is already gone is not an error: the row and the
// bytes are removed in that order, so a retry after a partial failure must succeed.
func (d *Disk) Remove(name string) error {
	path, err := d.pathFor(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("filestore: remove %s: %w", name, err)
	}
	return nil
}

// pathFor turns a storage name into a path, refusing anything that is not a name this
// package generated.
//
// The check is belt and braces — names come from NewName, not from a request — but it
// is the last line before a path reaches the filesystem, and "it cannot happen" is
// how traversal bugs get written.
func (d *Disk) pathFor(name string) (string, error) {
	if name == "" || len(name) != 32 || !isHex(name) {
		return "", fmt.Errorf("filestore: %w: %q", ErrNotFound, name)
	}
	if d.fanout {
		return filepath.Join(d.root, name[:2], name), nil
	}
	return filepath.Join(d.root, name), nil
}

// isHex reports whether s is entirely lower-case hexadecimal, which is what NewName
// produces and therefore contains no separator, no dot, and no traversal.
func isHex(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool {
		return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')
	}) < 0
}
