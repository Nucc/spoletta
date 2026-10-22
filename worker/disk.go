package main

// How much disk a run takes, checked after each of its steps against the
// most the webapp lets a run take (DiskLimit, its settings' Spending tab):
// its workspaces with their checkouts and pi sessions, its home and the
// files its steps generated. A step that leaves the run over its limit
// fails saying so, after its work is committed and brought back as any
// step's, so what it did isn't lost; the run's next steps fail so too until
// it takes less.
//
// What a file takes is the blocks it has on disk, not the size it says,
// so a sparse file counts as what it holds, and a file with several links
// counts once; links to elsewhere aren't followed. Engines can't hold a
// part of a volume to a size, so a step can still fill the volume while it
// runs; the check only keeps runs from piling up. Walking a run's
// directories takes a moment for checkouts of many files.

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strconv"
	"syscall"
)

// The directories a run's steps leave what they make in.
func runDirs(run int64) []string {
	dirs := []string{runHome(run), filepath.Join(filesRoot, "runs", strconv.FormatInt(run, 10))}
	for _, ws := range workspacesOf(run) {
		dirs = append(dirs, ws, sessionsDir(filepath.Base(ws)))
	}
	return dirs
}

// How many bytes the directories take on disk.
func diskUse(dirs []string) (int64, error) {
	type inode struct{ dev, ino uint64 }
	seen := map[inode]bool{}
	var total int64
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			info, err := d.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				total += info.Size()
				return nil
			}
			if st.Nlink > 1 && !info.IsDir() {
				key := inode{uint64(st.Dev), uint64(st.Ino)}
				if seen[key] {
					return nil
				}
				seen[key] = true
			}
			// Blocks of 512 bytes, on Linux and macOS alike.
			total += int64(st.Blocks) * 512
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// Fails the step's result if its run takes more disk than limit bytes; 0 is
// no limit.
func checkDisk(run, limit int64, res *runResult, l *stepLogger) {
	if run == 0 || limit <= 0 {
		return
	}
	used, err := diskUse(runDirs(run))
	if err != nil {
		l.infof("couldn't tell how much disk the run takes: %v", err)
		return
	}
	if used <= limit {
		return
	}
	msg := fmt.Sprintf("the run takes %s of disk, more than the %s it may", formatBytes(used), formatBytes(limit))
	l.infof("%s", msg)
	if res.Error != "" {
		msg = res.Error + "\n" + msg
	}
	res.OK, res.Submitted, res.Error = false, false, msg
}

// Bytes as people read them, in MB from a megabyte up.
func formatBytes(n int64) string {
	if n < 1<<20 {
		return fmt.Sprintf("%d KB", (n+1023)>>10)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
}
