package main

// The container engine sandboxes, podman and docker: each step's program
// runs in a container of its own, made through the engine's API on the
// socket mounted at CONTAINER_SOCKET, and removed once it exits. podman is
// spoken to in its own API, libpod's; docker in the Docker Engine API, from
// Docker 26 on, which mounts a part of a volume. podman's Docker-compatible
// API mounts the whole volume instead, silently, so podman is never spoken
// to in it, and every sandbox checks as the worker starts that a part of a
// volume is what its containers get (see probeSubpaths).
//
// A step's container runs as SANDBOX_USER (1000:1000), with a read-only
// root, every capability dropped, no new privileges, limits on memory, CPUs
// and processes, a tmpfs at /tmp, its home the run's (see runHome), and
// with SANDBOX_RUNTIME (such as runsc, gVisor) another OCI runtime. It
// reaches the network only through SANDBOX_NETWORK, an internal network
// the webapp is on and that goes nowhere else: its models, MCP servers and
// the internet hosts it may reach all go through the webapp's proxies.
// Committing what a step left gets no network at all.
//
// It sees only the step's own directories: the worker gives it a list (see
// sandboxSpec), each of which is a part of one of the worker's volumes,
// mounted at the same path, so nothing needs translating. Which volume
// holds which of the worker's directories the worker finds by inspecting
// its own container, SELF_CONTAINER or its hostname; it never mounts a
// path of its host. A worker that is root in its container, as it is to
// reach a rootless podman's socket, hands what it makes for a step to the
// sandbox's user; one that isn't must be that user.
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
	"maps"
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

// What steps' containers are made of and limited to, from the environment.
type containerConfig struct {
	image, user, network, runtime string
	memoryMB, cpus, pids          int64
	uid                           int
}

func readContainerConfig() (containerConfig, error) {
	c := containerConfig{
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
	}{{"SANDBOX_MEMORY_MB", &c.memoryMB, "4096"}, {"SANDBOX_CPUS", &c.cpus, "2"}, {"SANDBOX_PIDS", &c.pids, "1024"}} {
		if *v.to, err = strconv.ParseInt(envOr(v.name, v.def), 10, 64); err != nil || *v.to <= 0 {
			return c, fmt.Errorf("%s must be a positive number", v.name)
		}
	}
	uid, gid, ok := strings.Cut(c.user, ":")
	if c.uid, err = strconv.Atoi(uid); err != nil || !ok {
		return c, errors.New("SANDBOX_USER must be uid:gid, such as 1000:1000")
	}
	if _, err := strconv.Atoi(gid); err != nil {
		return c, errors.New("SANDBOX_USER must be uid:gid, such as 1000:1000")
	}
	// Root in its container hands the step what it makes; otherwise the
	// worker must be the step's user, for the step to write what it makes.
	switch euid := os.Geteuid(); {
	case euid == 0:
		sandboxOwner = c.uid
	case euid != c.uid:
		return c, fmt.Errorf("the worker runs as %d, so steps' containers must too: SANDBOX_USER is %s", euid, c.user)
	}
	return c, nil
}

// The environment of a step's program in a container: the image's PATH, a
// home of the run's or on the tmpfs, the root being read-only, and the
// internet through the webapp's proxy, which curl, git and npm take; the
// webapp itself, models and MCP servers, directly.
func containerEnv(spec sandboxSpec) map[string]string {
	env := map[string]string{}
	for _, kv := range spec.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "PATH", "HOME", "HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy", "NO_PROXY", "no_proxy":
			continue
		}
		env[k] = v
	}
	env["HOME"] = "/tmp"
	if spec.Home != "" {
		env["HOME"] = spec.Home
	}
	if spec.Egress != "" {
		for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
			env[k] = spec.Egress
		}
		if u, err := url.Parse(spec.Egress); err == nil {
			env["NO_PROXY"] = u.Hostname() + ",localhost,127.0.0.1"
			env["no_proxy"] = env["NO_PROXY"]
		}
	}
	return env
}

// The command, reading its stdin from the file the spec names, if any,
// and writing its stderr to stderrFile, if given.
func containerCmd(spec sandboxSpec, stderrFile string) []string {
	script := `exec "$@"`
	if spec.Stdin != "" {
		script += ` < "$STEP_STDIN"`
	}
	if stderrFile != "" {
		script += ` 2> "$STEP_STDERR"`
	}
	if spec.Stdin == "" && stderrFile == "" {
		return spec.Cmd
	}
	return append([]string{"/bin/sh", "-c", script, "sh"}, spec.Cmd...)
}

// The mounts in the order they go on, a directory inside another after it,
// each handed to the sandbox's user: a step's own directory whole, a
// workspace's or a run's top alone, what is inside being the step's already.
func orderedMounts(spec sandboxSpec) ([]mount, error) {
	mounts := slices.Clone(spec.Mounts)
	slices.SortFunc(mounts, func(a, b mount) int { return len(a.Path) - len(b.Path) })
	for _, m := range mounts {
		if err := ownForSandbox(m.Path, strings.HasPrefix(m.Path, stepsRoot+"/")); err != nil {
			return nil, err
		}
	}
	return mounts, nil
}

// The volume holding path, by where the worker mounts volumes, and where
// in it path is.
func volumeOf(volumes map[string]string, path string) (string, string, error) {
	best := ""
	for dest := range volumes {
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
	return volumes[best], rel, nil
}

// Checks the worker's directories steps get parts of are each on a volume.
func checkVolumes(volumes map[string]string) error {
	for _, dir := range []string{filesRoot, workspacesRoot, stepsRoot} {
		if _, _, err := volumeOf(volumes, dir); err != nil {
			return fmt.Errorf("%s must be a volume of the worker's, for steps' containers to be given parts of it", dir)
		}
	}
	return nil
}

// Checks the sandbox gives a container a part of a volume, not all of it:
// a container given a directory of steps' must find in it what is there,
// and nothing of the directory around it, or the worker doesn't start.
func probeSubpaths(s sandbox) error {
	dir, err := makeStepDir("private-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "inside"), []byte("x"), 0o600); err != nil {
		return err
	}
	if err := ownForSandbox(dir, true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out, errOut bytes.Buffer
	script := fmt.Sprintf(`test -f %q/inside && test ! -e %q/%s && echo part`, dir, dir, filepath.Base(dir))
	err = s.run(ctx, sandboxSpec{Cmd: []string{"/bin/sh", "-c", script}, Dir: "/tmp", Mounts: []mount{{Path: dir, ReadOnly: true}}, Offline: true}, &out, &errOut)
	if err != nil || strings.TrimSpace(out.String()) != "part" {
		return fmt.Errorf("a step's container wasn't given just a part of a volume, as its directories must be (%v %s): steps' containers would see other steps' directories", err, strings.TrimSpace(errOut.String()))
	}
	return nil
}

// The dialects of the engines' APIs.
type engineAPI int

const (
	libpodAPI engineAPI = iota
	dockerAPI
)

type engineSandbox struct {
	containerConfig
	api    engineAPI
	name   string
	client *http.Client
	// The worker's directories that are volumes, by where they are mounted.
	volumes map[string]string
}

func newEngineSandbox(ctx context.Context, api engineAPI) (sandbox, error) {
	c, err := readContainerConfig()
	if err != nil {
		return nil, err
	}
	s := &engineSandbox{containerConfig: c, api: api, name: "podman"}
	socket := envOr("CONTAINER_SOCKET", envOr("PODMAN_SOCKET", "/run/podman.sock"))
	if api == dockerAPI {
		s.name = "docker"
		socket = envOr("CONTAINER_SOCKET", "/var/run/docker.sock")
	}
	s.client = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	if err := s.call(ctx, http.MethodGet, "/_ping", nil, nil); err != nil {
		return nil, fmt.Errorf("%s at %s: %w", s.name, socket, err)
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
	if err := checkVolumes(s.volumes); err != nil {
		return nil, err
	}
	s.sweep(ctx)
	if err := probeSubpaths(s); err != nil {
		return nil, err
	}
	log.Printf("worker: steps run in %s containers of %s, as %s, on the network %s", s.name, s.image, s.user, s.network)
	return s, nil
}

// Removes the containers of steps the worker started and never removed.
func (s *engineSandbox) sweep(ctx context.Context) {
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

func (s *engineSandbox) remove(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.call(ctx, http.MethodDelete, "/containers/"+id+"?force=true&v=true", nil, nil); err != nil {
		log.Printf("worker: remove the step's container %s: %v", id[:min(12, len(id))], err)
	}
}

func (s *engineSandbox) run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error {
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
				path = "/containers/" + made.Id + "/stop?timeout=10&t=10"
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
	code, err := s.wait(context.WithoutCancel(ctx), made.Id)
	if err != nil {
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

// The exit status the container stopped with: libpod says it as a number,
// Docker as {"StatusCode": n}.
func (s *engineSandbox) wait(ctx context.Context, id string) (int, error) {
	if s.api == dockerAPI {
		var w struct{ StatusCode int }
		err := s.call(ctx, http.MethodPost, "/containers/"+id+"/wait?condition=not-running", nil, &w)
		return w.StatusCode, err
	}
	var code int
	err := s.call(ctx, http.MethodPost, "/containers/"+id+"/wait?condition=stopped&condition=exited", nil, &code)
	return code, err
}

// What the engine makes the step's container from: a libpod
// SpecGenerator, or a Docker container config.
func (s *engineSandbox) containerSpec(spec sandboxSpec) (map[string]any, error) {
	env := containerEnv(spec)
	cmd := containerCmd(spec, "")
	if spec.Stdin != "" {
		env["STEP_STDIN"] = spec.Stdin
	}
	mounts, err := orderedMounts(spec)
	if err != nil {
		return nil, err
	}
	if s.api == dockerAPI {
		return s.dockerSpec(spec, env, cmd, mounts)
	}
	var volumes []map[string]any
	for _, m := range mounts {
		name, sub, err := volumeOf(s.volumes, m.Path)
		if err != nil {
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
			"memory": map[string]any{"limit": s.memoryMB << 20},
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

func (s *engineSandbox) dockerSpec(spec sandboxSpec, env map[string]string, cmd []string, mounts []mount) (map[string]any, error) {
	var envList []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		envList = append(envList, k+"="+env[k])
	}
	var ms []map[string]any
	for _, m := range mounts {
		name, sub, err := volumeOf(s.volumes, m.Path)
		if err != nil {
			return nil, err
		}
		ms = append(ms, map[string]any{"Type": "volume", "Source": name, "Target": m.Path, "ReadOnly": m.ReadOnly, "VolumeOptions": map[string]any{"Subpath": sub}})
	}
	network := s.network
	if spec.Offline {
		network = "none"
	}
	host := map[string]any{
		"ReadonlyRootfs": true,
		"CapDrop":        []string{"ALL"},
		"SecurityOpt":    []string{"no-new-privileges"},
		"NetworkMode":    network,
		"Memory":         s.memoryMB << 20,
		"NanoCpus":       s.cpus * 1_000_000_000,
		"PidsLimit":      s.pids,
		"Tmpfs":          map[string]string{"/tmp": "rw,nosuid,nodev,size=2g,mode=1777"},
		"Mounts":         ms,
		"LogConfig":      map[string]any{"Type": "json-file"},
	}
	if s.runtime != "" {
		host["Runtime"] = s.runtime
	}
	return map[string]any{
		"Image":      s.image,
		"Cmd":        cmd,
		"Env":        envList,
		"WorkingDir": spec.Dir,
		"User":       s.user,
		"Labels":     map[string]string{sandboxLabel: "1"},
		"HostConfig": host,
	}, nil
}

// Splits an engine's stream of a container's output, a frame per write: a
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

// Calls the engine's API, decoding its answer into out, if any.
func (s *engineSandbox) call(ctx context.Context, method, path string, in, out any) error {
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

// The API's versions: libpod's of podman 5, Docker's of Docker 26, the
// first to mount a part of a volume.
func (s *engineSandbox) request(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	prefix := "/v5.0.0/libpod"
	if s.api == dockerAPI {
		prefix = "/v1.45"
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://engine"+prefix+path, body)
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
