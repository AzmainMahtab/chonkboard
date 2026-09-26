package filestore

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDisk(t *testing.T) *Disk {
	t.Helper()
	d, err := NewDisk(filepath.Join(t.TempDir(), "uploads"))
	require.NoError(t, err)
	return d
}

func TestSaveAndOpen(t *testing.T) {
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)

	written, err := d.Save(name, strings.NewReader("file contents"), 1024)
	require.NoError(t, err)
	assert.Equal(t, int64(13), written)

	f, err := d.Open(name)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()

	got, err := io.ReadAll(f)
	require.NoError(t, err)
	assert.Equal(t, "file contents", string(got))
}

func TestNewNameIsRandomAndPathSafe(t *testing.T) {
	// The storage name is never derived from what the uploader called the file: a
	// user-supplied name reaching the filesystem is a traversal bug, a collision
	// between two people uploading report.pdf, and on a case-insensitive filesystem
	// a way to overwrite somebody else's upload.
	seen := make(map[string]struct{}, 500)
	for range 500 {
		name, err := NewName()
		require.NoError(t, err)

		assert.Len(t, name, 32)
		assert.True(t, isHex(name), "%q must be hex only", name)
		assert.NotContains(t, name, "/")
		assert.NotContains(t, name, ".")

		_, dup := seen[name]
		require.False(t, dup, "generated the same name twice")
		seen[name] = struct{}{}
	}
}

func TestSaveRefusesAnOversizedFile(t *testing.T) {
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)

	_, err = d.Save(name, bytes.NewReader(make([]byte, 2048)), 1024)

	assert.ErrorIs(t, err, ErrTooLarge)

	// A partial file would sit there unreferenced until somebody audited the
	// directory.
	_, err = d.Open(name)
	assert.ErrorIs(t, err, ErrNotFound, "the partial write must be cleaned up")
}

func TestSaveAtExactlyTheLimitIsAllowed(t *testing.T) {
	// Reading one byte past the ceiling is how "exactly at the limit" is told from
	// "over it"; an off-by-one here refuses a legitimate upload.
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)

	written, err := d.Save(name, bytes.NewReader(make([]byte, 1024)), 1024)

	require.NoError(t, err)
	assert.Equal(t, int64(1024), written)
}

func TestSaveRefusesToOverwrite(t *testing.T) {
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)
	_, err = d.Save(name, strings.NewReader("first"), 1024)
	require.NoError(t, err)

	_, err = d.Save(name, strings.NewReader("second"), 1024)
	require.Error(t, err, "O_EXCL: a generated name must never silently overwrite")

	f, err := d.Open(name)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	got, _ := io.ReadAll(f)
	assert.Equal(t, "first", string(got))
}

// TestPathTraversalIsRefused is the reason pathFor validates at all. Names come from
// NewName rather than from a request, but this is the last line before a path reaches
// the filesystem, and "it cannot happen" is how traversal bugs get written.
func TestPathTraversalIsRefused(t *testing.T) {
	d := newDisk(t)

	for _, name := range []string{
		"", "..", "../etc/passwd", "../../../../etc/passwd",
		"subdir/file", `..\windows\system32`, "/etc/passwd",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",   // 31 chars
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", // 33 chars
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",  // upper case
		"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",  // not hex
		"../../aaaaaaaaaaaaaaaaaaaaaaaaaa",
		"0123456789abcdef0123456789abcde/", // trailing separator
	} {
		t.Run("name "+name, func(t *testing.T) {
			_, err := d.Open(name)
			assert.ErrorIs(t, err, ErrNotFound)

			_, err = d.Save(name, strings.NewReader("x"), 1024)
			assert.ErrorIs(t, err, ErrNotFound)

			assert.ErrorIs(t, d.Remove(name), ErrNotFound)
		})
	}
}

func TestNothingEscapesTheRoot(t *testing.T) {
	// Belt and braces on the above: whatever Save writes must be inside the root.
	root := filepath.Join(t.TempDir(), "uploads")
	d, err := NewDisk(root)
	require.NoError(t, err)

	name, err := NewName()
	require.NoError(t, err)
	_, err = d.Save(name, strings.NewReader("x"), 1024)
	require.NoError(t, err)

	var found []string
	require.NoError(t, filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			found = append(found, path)
		}
		return nil
	}))
	require.Len(t, found, 1)
	assert.True(t, strings.HasPrefix(found[0], root),
		"%s escaped the root %s", found[0], root)
}

func TestFilesAreFannedOut(t *testing.T) {
	// A single directory with tens of thousands of entries is slow to list and, on
	// some filesystems, slow to open a file in.
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)
	_, err = d.Save(name, strings.NewReader("x"), 1024)
	require.NoError(t, err)

	path, err := d.pathFor(name)
	require.NoError(t, err)
	assert.Equal(t, name[:2], filepath.Base(filepath.Dir(path)))
}

func TestOpenAndRemoveOnAMissingFile(t *testing.T) {
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)

	_, err = d.Open(name)
	assert.ErrorIs(t, err, ErrNotFound)

	// Already gone is not an error: the row and the bytes are removed in that
	// order, so a retry after a partial failure has to succeed.
	assert.NoError(t, d.Remove(name))
}

func TestRemove(t *testing.T) {
	d := newDisk(t)
	name, err := NewName()
	require.NoError(t, err)
	_, err = d.Save(name, strings.NewReader("x"), 1024)
	require.NoError(t, err)

	require.NoError(t, d.Remove(name))
	_, err = d.Open(name)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestNewDiskRefusesAnEmptyDirectory(t *testing.T) {
	_, err := NewDisk("")
	assert.Error(t, err)
}

func TestNewDiskCreatesTheDirectory(t *testing.T) {
	// So `docker compose up` on a fresh volume works, and so does a first run on a
	// clean clone.
	dir := filepath.Join(t.TempDir(), "deep", "nested", "uploads")
	_, err := NewDisk(dir)
	require.NoError(t, err)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}
