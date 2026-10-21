package main

// The podman sandbox: each step's program runs in a container of its own,
// made through the API of a rootless podman whose socket is mounted at
// PODMAN_SOCKET, and removed once it exits. It runs as SANDBOX_USER
// (1000:1000), with a read-only root, every capability dropped, no new
// privileges, limits on memory, CPUs and processes, a tmpfs at /tmp that
// is its home, the default seccomp profile, and with SANDBOX_RUNTIME (such
// as runsc, gVisor) another OCI runtime. It reaches the network only
// through SANDBOX_NETWORK, an internal network the webapp is on and that
// goes nowhere else: its models, MCP servers and the internet hosts it may
// reach all go through the webapp's proxies. Committing what a step left
// gets no network at all.
//
// It sees only the step's own directories: the worker gives it a list (see
// sandboxSpec), each of which is a part of one of the worker's volumes,
// mounted at the same path, so nothing needs translating. Which volume
// holds which of the worker's directories the worker finds by inspecting
// its own container, SELF_CONTAINER or its hostname; it never mounts a
// path of its host. Everything the worker makes for a step is handed to
// the sandbox's user, which the worker, root in its container and so the
// podman user outside it, can do.
//
// Without the socket, or its own container's volumes, the worker doesn't
// start: it never falls back to running a step itself. Containers it
// started and didn't remove, the worker stopping meanwhile, carry a label
// it removes them by as it starts.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const sandboxLabel = "spoletta.sandbox"

type podmanSandbox struct {
	client               *http.Client
	image, user, network string
	runtime              string
	memory, cpus, pids   int64
	// The worker's directories that are volumes, by where they are mounted.
	volumes map[string]string
}

func newPodmanSandbox(ctx context.Context) (*podmanSandbox, error) {
	socket := envOr("PODMAN_SOCKET", "/run/podman.sock")
	s := &podmanSandbox{
		client: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}}},
		image:   envOr("SANDBOX_IMAGE", "localhost/spoletta-sandbox:latest"),
		user:    envOr("SANDBOX_USER", "1000:1000"),
		network: envOr("SANDBOX_NETWORK", "spoletta-sandbox"),
		runtime: os.Getenv("SANDBOX_RUNTIME"),
	}
	var err error
	for _, v := range []struct {
		name string
		to   *int64
		def  string
	}{{"SANDBOX_MEMORY_MB", &s.memory, "4096"}, {"SANDBOX_CPUS", &s.cpus, "2"}, {"SANDBOX_PIDS", &s.pids, "1024"}} {
		if *v.to, err = strconv.ParseInt(envOr(v.name, v.def), 10, 64); err != nil || *v.to <= 0 {
			return nil, fmt.Errorf("%s must be a positive number", v.name)
		}
	}
	uid, _, _ := strings.Cut(s.user, ":")
	if sandboxOwner, err = strconv.Atoi(uid); err != nil {
		return nil, fmt.Errorf("SANDBOX_USER must be uid:gid, such as 1000:1000")
	}
	if err := s.call(ctx, http.MethodGet, "/_ping", nil, nil); err != nil {
		return nil, fmt.Errorf("podman at %s: %w", socket, err)
	}
	self := os.Getenv("SELF_CONTAINER")
	if self == "" {
		self, _ = os.Hostname()
	}
	var inspect struct {
		Mounts []struct {
			Type, Name, Destination string
		}
	}
	if err := s.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(self)+"/json", nil, &inspect); err != nil {
		return nil, fmt.Errorf("inspect the worker's own container %q (SELF_CONTAINER): %w", self, err)
	}
	s.volumes = map[string]string{}
	for _, m := range inspect.Mounts {
		if m.Type == "volume" {
			s.volumes[m.Destination] = m.Name
		}
	}
	for _, dir := range []string{filesRoot, workspacesRoot, stepsRoot} {
		if _, _, err := s.volumeOf(dir); err != nil {
			return nil, fmt.Errorf("%s must be a volume of the worker's, for steps' containers to be given parts of it", dir)
		}
	}
	s.sweep(ctx)
	log.Printf("worker: steps run in podman containers of %s, as %s, on the network %s", s.image, s.user, s.network)
	return s, nil
}

// The volume holding path, and where in it path is.
func (s *podmanSandbox) volumeOf(path string) (string, string, error) {
	best := ""
	for dest := range s.volumes {
		if (path == dest || strings.HasPrefix(path, dest+"/")) && len(dest) > len(best) {
			best = dest
		}
	}
	if best == "" {
		return "", "", fmt.Errorf("%s is on none of the worker's volumes", path)
	}
	rel, _ := filepath.Rel(best, path)
	if rel == "." {
		rel = ""
	}
	return s.volumes[best], rel, nil
}

// Removes the containers of steps the worker started and never removed.
func (s *podmanSandbox) sweep(ctx context.Context) {
	filters, _ := json.Marshal(map[string][]string{"label": {sandboxLabel}})
	var left []struct{ Id string }
	if err := s.call(ctx, http.MethodGet, "/containers/json?all=true&filters="+url.QueryEscape(string(filters)), nil, &left); err != nil {
		log.Printf("worker: list steps' containers left behind: %v", err)
		return
	}
	for _, c := range left {
		s.remove(c.Id)
	}
	if len(left) > 0 {
		log.Printf("worker: removed %d steps' containers left behind", len(left))
	}
}

func (s *podmanSandbox) remove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.call(ctx, http.MethodDelete, "/containers/"+id+"?force=true&v=true", nil, nil); err != nil {
		log.Printf("worker: remove the step's container %s: %v", id[:min(12, len(id))], err)
	}
}

func (s *podmanSandbox) run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error {
	create, err := s.containerSpec(spec)
	if err != nil {
		return err
	}
	var made struct{ Id string }
	if err := s.call(ctx, http.MethodPost, "/containers/create", create, &made); err != nil {
		return fmt.Errorf("make the step's container: %w", err)
	}
	defer s.remove(made.Id)
	if err := s.call(ctx, http.MethodPost, "/containers/"+made.Id+"/start", nil, nil); err != nil {
		return fmt.Errorf("start the step's container: %w", err)
	}
	// Stopping the step stops its container: pi gets to stop its tools first.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			bg, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			path := "/containers/" + made.Id + "/kill?signal=SIGKILL"
			if spec.Graceful {
				path = "/containers/" + made.Id + "/stop?timeout=10"
			}
			s.call(bg, http.MethodPost, path, nil, nil)
		case <-stopped:
		}
	}()
	// The logs end as the container does.
	logs, err := s.request(context.WithoutCancel(ctx), http.MethodGet, "/containers/"+made.Id+"/logs?follow=true&stdout=true&stderr=true", nil)
	if err != nil {
		return fmt.Errorf("follow the step's container: %w", err)
	}
	err = demux(logs.Body, stdout, stderr)
	logs.Body.Close()
	if err != nil {
		return fmt.Errorf("read the step's container: %w", err)
	}
	var code int
	if err := s.call(context.WithoutCancel(ctx), http.MethodPost, "/containers/"+made.Id+"/wait?condition=stopped&condition=exited", nil, &code); err != nil {
		return fmt.Errorf("wait for the step's container: %w", err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

// What podman makes the step's container from, a libpod SpecGenerator.
func (s *podmanSandbox) containerSpec(spec sandboxSpec) (map[string]any, error) {
	env := map[string]string{}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		// The image's own, and a home on the tmpfs, the root being read-only.
		case "PATH", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy":
			continue
		}
		env[k] = v
	}
	env["HOME"] = "/tmp"
	// The internet through the webapp's proxy, which curl, git and npm
	// take; the webapp itself, models and MCP servers, directly.
	if spec.Egress != "" {
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[k] = spec.Egress
		}
		if u, err := url.Parse(spec.Egress); err == nil {
			env["NO_PROXY"] = u.Hostname() + ",localhost,127.0.0.1"
			env["no_proxy"] = env["NO_PROXY"]
		}
	}
	cmd := spec.Cmd
	if spec.Stdin != "" {
		cmd = append([]string{"/bin/sh", "-c", `exec "$@" < "$0"`, spec.Stdin}, cmd...)
	}
	mounts := slices.Clone(spec.Mounts)
	// A directory inside another is mounted after it, over it.
	slices.SortFunc(mounts, func(a, b mount) int { return len(a.Path) - len(b.Path) })
	var volumes []map[string]any
	for _, m := range mounts {
		name, sub, err := s.volumeOf(m.Path)
		if err != nil {
			return nil, err
		}
		if err := ownForSandbox(m.Path, strings.HasPrefix(m.Path, stepsRoot+"/")); err != nil {
			return nil, err
		}
		v := map[string]any{"Name": name, "Dest": m.Path, "SubPath": sub, "Options": []string{"rw"}}
		if m.ReadOnly {
			v["Options"] = []string{"ro"}
		}
		volumes = append(volumes, v)
	}
	c := map[string]any{
		"image":                s.image,
		"command":              cmd,
		"env":                  env,
		"work_dir":             spec.Dir,
		"user":                 s.user,
		"read_only_filesystem": true,
		"cap_drop":             []string{"ALL"},
		"no_new_privileges":    true,
		"volumes":              volumes,
		"mounts":               []map[string]any{{"destination": "/tmp", "type": "tmpfs", "source": "tmpfs", "options": []string{"rw", "nosuid", "nodev", "size=2g", "mode=1777"}}},
		"resource_limits": map[string]any{
			"memory": map[string]any{"limit": s.memory << 20},
			"cpu":    map[string]any{"quota": s.cpus * 100000, "period": 100000},
			"pids":   map[string]any{"limit": s.pids},
		},
		"labels":            map[string]string{sandboxLabel: "1"},
		"log_configuration": map[string]any{"driver": "k8s-file"},
	}
	if spec.Offline {
		c["netns"] = map[string]string{"nsmode": "none"}
	} else {
		c["netns"] = map[string]string{"nsmode": "bridge"}
		c["networks"] = map[string]any{s.network: map[string]any{}}
	}
	if s.runtime != "" {
		c["oci_runtime"] = s.runtime
	}
	return c, nil
}

// Splits podman's stream of a container's output, a frame per write: a
// byte saying stdout (1) or stderr (2), three zeros, and the length.
func demux(r io.Reader, stdout, stderr io.Writer) error {
	br := bufio.NewReader(r)
	var header [8]byte
	for {
		if _, err := io.ReadFull(br, header[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		w := stdout
		if header[0] == 2 {
			w = stderr
		}
		if _, err := io.CopyN(w, br, int64(binary.BigEndian.Uint32(header[4:]))); err != nil {
			return err
		}
	}
}

// Calls podman's API, decoding its answer into out, if any.
func (s *podmanSandbox) call(ctx context.Context, method, path string, in, out any) error {
	resp, err := s.request(ctx, method, path, in)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (s *podmanSandbox) request(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://podman/v5.0.0/libpod"+path, body)
	if err != nil {
		return nil, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var e struct {
			Message string `json:"message"`
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Message == "" {
			e.Message = strings.TrimSpace(string(b))
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, e.Message)
	}
	return resp, nil
}
