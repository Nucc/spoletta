package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file counts as the blocks it takes, once however many links it has; a
// link elsewhere isn't followed.
func TestDiskUse(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, n int) {
		os.WriteFile(filepath.Join(dir, name), make([]byte, n), 0o644)
	}
	write("a", 1<<20)
	base, err := diskUse([]string{dir})
	if err != nil || base < 1<<20 {
		t.Fatalf("a megabyte takes %d: %v", base, err)
	}
	// Linked again, and a link to a directory with much in it.
	os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "b"))
	elsewhere := t.TempDir()
	os.WriteFile(filepath.Join(elsewhere, "big"), make([]byte, 4<<20), 0o644)
	os.Symlink(elsewhere, filepath.Join(dir, "away"))
	// A sparse file saying a gigabyte, holding nothing.
	f, _ := os.Create(filepath.Join(dir, "sparse"))
	f.Truncate(1 << 30)
	f.Close()
	got, err := diskUse([]string{dir, filepath.Join(dir, "missing")})
	if err != nil || got > base+(256<<10) {
		t.Errorf("with a second link, a link away and a sparse file it takes %d, was %d: %v", got, base, err)
	}
}

// A step that leaves its run over its limit fails, saying what it takes.
func TestCheckDisk(t *testing.T) {
	workspacesRoot, filesRoot = t.TempDir(), t.TempDir()
	ws := filepath.Join(workspacesRoot, "run-7")
	os.MkdirAll(filepath.Join(ws, "api"), 0o755)
	os.WriteFile(filepath.Join(ws, "api", "build.out"), make([]byte, 2<<20), 0o644)
	os.MkdirAll(runHome(7), 0o755)
	os.WriteFile(filepath.Join(runHome(7), "cache"), make([]byte, 1<<20), 0o644)

	res := runResult{OK: true, Output: "done", Submitted: true}
	checkDisk(7, 8<<20, &res, nil)
	if !res.OK {
		t.Errorf("under its limit, the step %+v", res)
	}
	checkDisk(7, 2<<20, &res, nil)
	if res.OK || res.Submitted || !strings.HasPrefix(res.Error, "the run takes 3.") || !strings.HasSuffix(res.Error, "of disk, more than the 2.0 MB it may") {
		t.Errorf("over its limit, the step %+v", res)
	}
	// A run with nothing on disk is under any limit.
	other := runResult{OK: true}
	checkDisk(8, 1, &other, nil)
	if !other.OK {
		t.Errorf("an empty run %+v", other)
	}
}
