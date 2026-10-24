package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// What a step changed in a repository, for a person to look over: the files
// between the commit it started from and the one it left, each with its
// patch. It is read from the shared repository, where the runs' branches
// are brought back, never from a checkout a step had (see workspace.go),
// and as a diff of two trees, which needs none of the history the shared
// repository doesn't keep.
type diffRequest struct {
	// The workspace the step worked in, such as run-42, whose kept meta says
	// which repository the name is.
	Workspace  string `json:"workspace"`
	Repository string `json:"repository"`
	Before     string `json:"before"`
	After      string `json:"after"`
}

type fileChange struct {
	Path string `json:"path"`
	// For a file renamed or copied: where it came from.
	OldPath string `json:"oldPath,omitempty"`
	// added, modified, deleted, renamed, copied or type changed.
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary,omitempty"`
	// The file's part of the diff, from its diff --git line; empty and
	// omitted set when it is too large to show.
	Patch   string `json:"patch"`
	Omitted bool   `json:"omitted,omitempty"`
}

type repoDiff struct {
	Files []fileChange `json:"files"`
	// How many files changed past the ones listed.
	More int `json:"more"`
}

const (
	// The most files listed; a step that committed what it installed changes
	// tens of thousands.
	maxDiffFiles = 1000
	// The most of the patch read, and of one file's part of it.
	maxDiffBytes     = 4 << 20
	maxFilePatchSize = 512 << 10
	diffTimeout      = 30 * time.Second
)

var errUnknownWorkspace = errors.New("the worker keeps no such repository of the workspace")

func diffWorkspace(ctx context.Context, req diffRequest) (repoDiff, error) {
	if !safeID.MatchString(req.Workspace) || !safeName.MatchString(req.Repository) {
		return repoDiff{}, errors.New("invalid workspace or repository")
	}
	if !commitID.MatchString(req.Before) || !commitID.MatchString(req.After) {
		return repoDiff{}, errors.New("invalid commit")
	}
	meta, err := readMeta(req.Workspace)
	if err != nil {
		return repoDiff{}, err
	}
	url, ok := meta.Repositories[req.Repository]
	if !ok {
		return repoDiff{}, errUnknownWorkspace
	}
	ctx, cancel := context.WithTimeout(ctx, diffTimeout)
	defer cancel()
	return diffCommits(ctx, sharedRepo(url), req.Before, req.After)
}

// The worker's own git config may say otherwise; a diff here is always the same.
var diffArgs = []string{
	"-c", "diff.noprefix=false", "-c", "diff.mnemonicPrefix=false",
	"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--find-renames", "--src-prefix=a/", "--dst-prefix=b/",
}

func diffCommits(ctx context.Context, repo, before, after string) (repoDiff, error) {
	for _, c := range []string{before, after} {
		if gitOK(ctx, repo, "cat-file", "-e", c+"^{commit}") != nil {
			return repoDiff{}, fmt.Errorf("commit %s isn't in the repository any more", c[:7])
		}
	}
	status, _, err := gitLimited(ctx, repo, -1, append(diffArgs, "--name-status", "-z", before, after)...)
	if err != nil {
		return repoDiff{}, err
	}
	numstat, _, err := gitLimited(ctx, repo, -1, append(diffArgs, "--numstat", "-z", before, after)...)
	if err != nil {
		return repoDiff{}, err
	}
	files, err := parseNameStatus(status)
	if err != nil {
		return repoDiff{}, err
	}
	counts, err := parseNumstat(numstat)
	if err == nil && len(counts) == len(files) {
		for i, c := range counts {
			files[i].Additions, files[i].Deletions, files[i].Binary = c.additions, c.deletions, c.binary
		}
	}
	d := repoDiff{Files: files}
	if len(files) > maxDiffFiles {
		d.Files, d.More = files[:maxDiffFiles], len(files)-maxDiffFiles
	}
	patch, cut, err := gitLimited(ctx, repo, maxDiffBytes, append(diffArgs, before, after)...)
	if err != nil {
		return repoDiff{}, err
	}
	parts := splitPatch(patch)
	// The parts come in the order the files do; one cut short isn't shown,
	// nor what came after it.
	if cut && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > len(files) {
		parts = nil
	}
	for i := range d.Files {
		if i >= len(parts) || len(parts[i]) > maxFilePatchSize {
			d.Files[i].Omitted = true
			continue
		}
		d.Files[i].Patch = parts[i]
	}
	return d, nil
}

var statusWords = map[byte]string{
	'A': "added", 'M': "modified", 'D': "deleted", 'R': "renamed", 'C': "copied", 'T': "type changed",
}

// --name-status -z: a status, then the path, or the old and new paths for a
// rename or copy, each ending in a NUL.
func parseNameStatus(out []byte) ([]fileChange, error) {
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	files := []fileChange{}
	for i := 0; i < len(fields) && fields[i] != ""; {
		s := fields[i]
		word, ok := statusWords[s[0]]
		if !ok {
			word = "modified"
		}
		f := fileChange{Status: word}
		if s[0] == 'R' || s[0] == 'C' {
			if i+2 >= len(fields) {
				return nil, errors.New("git diff: unexpected --name-status output")
			}
			f.OldPath, f.Path = fields[i+1], fields[i+2]
			i += 3
		} else {
			if i+1 >= len(fields) {
				return nil, errors.New("git diff: unexpected --name-status output")
			}
			f.Path = fields[i+1]
			i += 2
		}
		files = append(files, f)
	}
	return files, nil
}

type lineCounts struct {
	additions, deletions int
	binary               bool
}

// --numstat -z: "added\tdeleted\tpath" ending in a NUL, or for a rename or
// copy "added\tdeleted\t" and then the old and new paths, each ending in a
// NUL; a binary file's counts are -.
func parseNumstat(out []byte) ([]lineCounts, error) {
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var counts []lineCounts
	for i := 0; i < len(fields) && fields[i] != ""; i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			return nil, errors.New("git diff: unexpected --numstat output")
		}
		var c lineCounts
		if parts[0] == "-" {
			c.binary = true
		} else {
			c.additions, _ = strconv.Atoi(parts[0])
			c.deletions, _ = strconv.Atoi(parts[1])
		}
		if parts[2] == "" {
			i += 2
		}
		counts = append(counts, c)
	}
	return counts, nil
}

// Cuts a patch into each file's part. A part starts with a diff --git line;
// a line of a file's content can't, as each starts with a space, + or -.
func splitPatch(patch []byte) []string {
	var parts []string
	start := -1
	for i := 0; i < len(patch); {
		end := bytes.IndexByte(patch[i:], '\n')
		if end < 0 {
			end = len(patch)
		} else {
			end += i + 1
		}
		if bytes.HasPrefix(patch[i:], []byte("diff --git ")) {
			if start >= 0 {
				parts = append(parts, string(patch[start:i]))
			}
			start = i
		}
		i = end
	}
	if start >= 0 {
		parts = append(parts, string(patch[start:]))
	}
	return parts
}

// Runs git and returns what it printed, at most limit bytes of it unless
// limit is negative, and whether there was more, which stops git.
func gitLimited(ctx context.Context, dir string, limit int64, args ...string) ([]byte, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = childEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	var r io.Reader = stdout
	if limit >= 0 {
		r = io.LimitReader(stdout, limit+1)
	}
	out, readErr := io.ReadAll(r)
	cut := limit >= 0 && int64(len(out)) > limit
	if cut {
		out = out[:limit]
		cancel()
	}
	err = cmd.Wait()
	if cut {
		return out, true, nil
	}
	if readErr != nil {
		return nil, false, readErr
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, false, fmt.Errorf("git diff: %s", msg)
		}
		return nil, false, fmt.Errorf("git diff: %w", err)
	}
	return out, false, nil
}
