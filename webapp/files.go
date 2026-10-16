package main

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// Files that runs generate live in a volume the worker writes and the
// webapp reads, mounted at the same path in both: each step writes its files
// in a directory of its own, and an object's file attributes hold their
// paths there.
var filesRoot = env("FILES_DIR", "/data/files")

// The paths of files under the files root that a generated object's content
// names, in the order they appear.
func filesIn(content string) []string {
	// Read token by token, as decoding into maps would lose the order of an
	// object's keys. An open object's next string is a key when key is set.
	type level struct{ object, key bool }
	var open []level
	var paths []string
	dec := json.NewDecoder(strings.NewReader(content))
	for {
		t, err := dec.Token()
		if err == io.EOF && len(open) == 0 {
			return paths
		}
		if err != nil {
			return nil
		}
		n := len(open)
		if _, ok := t.(string); ok && n > 0 && open[n-1].key {
			open[n-1].key = false
			continue
		}
		switch t := t.(type) {
		case json.Delim:
			switch t {
			case '{':
				open = append(open, level{object: true, key: true})
				continue
			case '[':
				open = append(open, level{})
				continue
			}
			open = open[:n-1]
		case string:
			if strings.HasPrefix(t, filesRoot+"/") {
				paths = append(paths, t)
			}
		}
		// A value ended, so the object it's in has a key next.
		if n := len(open); n > 0 && open[n-1].object {
			open[n-1].key = true
		}
	}
}

func registerFileRoutes(mux *http.ServeMux) {
	// A generated file, by its path. Opened through a root, so a symlink an
	// agent left can't reach outside the files.
	mux.HandleFunc("GET /api/files", func(w http.ResponseWriter, r *http.Request) {
		rel, err := filepath.Rel(filesRoot, filepath.Clean(r.URL.Query().Get("path")))
		if err != nil || !filepath.IsLocal(rel) {
			http.Error(w, "not a generated file", http.StatusBadRequest)
			return
		}
		root, err := os.OpenRoot(filesRoot)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		defer root.Close()
		info, err := fs.Stat(root.FS(), filepath.ToSlash(rel))
		if errors.Is(err, fs.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		if err != nil {
			serverError(w, err)
			return
		}
		// What an agent wrote may be HTML; sandboxed, it can't act as the app.
		w.Header().Set("Content-Security-Policy", "sandbox")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFileFS(w, r, root.FS(), filepath.ToSlash(rel))
	})
}
