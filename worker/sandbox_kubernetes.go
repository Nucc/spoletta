package main

// The kubernetes sandbox: each step's program runs in a pod of its own,
// made through the cluster's API with the worker's service account, which
// may create, read, follow the log of and delete pods in its namespace
// (SANDBOX_NAMESPACE, else its own), and nothing else; see docs/SANDBOX.md.
// The pod runs as SANDBOX_USER, non-root, with a read-only root, every
// capability dropped, no privilege escalation, the runtime's default
// seccomp profile, memory and CPU limits, a memory-backed /tmp, no service
// account token and no service links, and with SANDBOX_RUNTIME a runtime
// class, such as gVisor's.
//
// It sees only the step's own directories: each is a part of one of the
// persistent volume claims the worker mounts, found by reading the
// worker's own pod (POD_NAME, else its hostname), mounted at the same path
// with a subPath. Claims that can be mounted on one node only keep the
// step's pod on the worker's node, unless SANDBOX_SAME_NODE is false. The
// worker runs as the steps' user, so what it makes is theirs.
//
// The network is the cluster's to fence: steps' pods carry the label
// spoletta.sandbox/network, webapp for those that may reach the webapp and
// offline for those that may reach nothing, which network policies (in
// docs/SANDBOX.md) hold them to. A pod's stdout comes from its log, which
// interleaves stderr, so stderr goes to a file of the worker's instead.
//
// Without the API, or the worker's own pod's claims, the worker doesn't
// start; pods it started and didn't delete, the worker stopping meanwhile,
// carry its name in a label and are deleted as it starts.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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
	"strings"
	"time"
)

const (
	serviceAccountDir = "/var/run/secrets/kubernetes.io/serviceaccount"
	sandboxWorker     = "spoletta.sandbox/worker"
	sandboxNetwork    = "spoletta.sandbox/network"
)

// A part of a claim the worker mounts at a directory.
type claimPart struct {
	claim, sub string
}

type kubeSandbox struct {
	containerConfig
	api, namespace, self, node string
	tokenFile                  string
	client                     *http.Client
	// The worker's directories that are claims, by where they are mounted.
	claims map[string]claimPart
}

func newKubeSandbox(ctx context.Context) (sandbox, error) {
	c, err := readContainerConfig()
	if err != nil {
		return nil, err
	}
	s := &kubeSandbox{containerConfig: c, tokenFile: envOr("KUBE_TOKEN_FILE", serviceAccountDir+"/token")}
	s.api = os.Getenv("KUBE_API_URL")
	if s.api == "" {
		host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
		if host == "" {
			return nil, errors.New("not in a Kubernetes cluster: KUBERNETES_SERVICE_HOST isn't set, nor KUBE_API_URL")
		}
		s.api = "https://" + net.JoinHostPort(host, port)
	}
	pool := x509.NewCertPool()
	if ca, err := os.ReadFile(envOr("KUBE_CA_FILE", serviceAccountDir+"/ca.crt")); err == nil {
		pool.AppendCertsFromPEM(ca)
	} else {
		return nil, fmt.Errorf("the cluster's certificate: %w", err)
	}
	s.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	s.namespace = os.Getenv("SANDBOX_NAMESPACE")
	if s.namespace == "" {
		b, err := os.ReadFile(serviceAccountDir + "/namespace")
		if err != nil {
			return nil, errors.New("SANDBOX_NAMESPACE isn't set, and the service account's namespace can't be read")
		}
		s.namespace = strings.TrimSpace(string(b))
	}
	s.self = os.Getenv("POD_NAME")
	if s.self == "" {
		s.self, _ = os.Hostname()
	}
	var pod struct {
		Spec struct {
			NodeName   string `json:"nodeName"`
			Containers []struct {
				VolumeMounts []struct {
					Name      string `json:"name"`
					MountPath string `json:"mountPath"`
					SubPath   string `json:"subPath"`
				} `json:"volumeMounts"`
			} `json:"containers"`
			Volumes []struct {
				Name                  string `json:"name"`
				PersistentVolumeClaim *struct {
					ClaimName string `json:"claimName"`
				} `json:"persistentVolumeClaim"`
			} `json:"volumes"`
		} `json:"spec"`
	}
	if err := s.call(ctx, http.MethodGet, s.pods()+"/"+url.PathEscape(s.self), nil, &pod); err != nil {
		return nil, fmt.Errorf("read the worker's own pod %q (POD_NAME): %w", s.self, err)
	}
	if os.Getenv("SANDBOX_SAME_NODE") != "false" {
		s.node = pod.Spec.NodeName
	}
	claimOf := map[string]string{}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			claimOf[v.Name] = v.PersistentVolumeClaim.ClaimName
		}
	}
	s.claims = map[string]claimPart{}
	for _, ct := range pod.Spec.Containers {
		for _, m := range ct.VolumeMounts {
			if claim, ok := claimOf[m.Name]; ok {
				s.claims[m.MountPath] = claimPart{claim, m.SubPath}
			}
		}
	}
	volumes := map[string]string{}
	for dest, p := range s.claims {
		volumes[dest] = p.claim
	}
	if err := checkVolumes(volumes); err != nil {
		return nil, err
	}
	s.sweep(ctx)
	if err := probeSubpaths(s); err != nil {
		return nil, err
	}
	log.Printf("worker: steps run in pods of %s in the namespace %s, as %s", s.image, s.namespace, s.user)
	return s, nil
}

func (s *kubeSandbox) pods() string {
	return "/api/v1/namespaces/" + url.PathEscape(s.namespace) + "/pods"
}

// The claim holding path, and where in it path is.
func (s *kubeSandbox) claimOf(path string) (claimPart, error) {
	best := ""
	for dest := range s.claims {
		if (path == dest || strings.HasPrefix(path, dest+"/")) && len(dest) > len(best) {
			best = dest
		}
	}
	if best == "" {
		return claimPart{}, fmt.Errorf("%s is on none of the worker's claims", path)
	}
	rel, _ := filepath.Rel(best, path)
	p := s.claims[best]
	if rel != "." {
		p.sub = filepath.Join(p.sub, rel)
	}
	return p, nil
}

// Deletes the pods of steps the worker started and never deleted.
func (s *kubeSandbox) sweep(ctx context.Context) {
	var left struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		} `json:"items"`
	}
	selector := url.QueryEscape(sandboxWorker + "=" + labelValue(s.self))
	if err := s.call(ctx, http.MethodGet, s.pods()+"?labelSelector="+selector, nil, &left); err != nil {
		log.Printf("worker: list steps' pods left behind: %v", err)
		return
	}
	for _, p := range left.Items {
		s.remove(p.Metadata.Name, 0)
	}
	if len(left.Items) > 0 {
		log.Printf("worker: deleted %d steps' pods left behind", len(left.Items))
	}
}

func (s *kubeSandbox) remove(name string, grace int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	err := s.call(ctx, http.MethodDelete, s.pods()+"/"+name, map[string]any{"gracePeriodSeconds": grace, "propagationPolicy": "Background"}, nil)
	if err != nil && !strings.HasPrefix(err.Error(), "404") {
		log.Printf("worker: delete the step's pod %s: %v", name, err)
	}
}

// A label's value: at most 63 letters, digits, -, _ and .
func labelValue(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, s)
	if len(s) > 63 {
		s = s[:63]
	}
	return strings.Trim(s, "-_.")
}

func (s *kubeSandbox) run(ctx context.Context, spec sandboxSpec, stdout, stderr io.Writer) error {
	// The pod's log is its stdout and its stderr together, so its stderr
	// goes to a file of the step's, read once it is over.
	errDir, err := makeStepDir("private-stderr-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(errDir)
	errFile := filepath.Join(errDir, "stderr")
	spec.Mounts = append(slices.Clone(spec.Mounts), mount{Path: errDir})
	pod, err := s.podSpec(spec, errFile)
	if err != nil {
		return err
	}
	name := pod["metadata"].(map[string]any)["name"].(string)
	if err := s.call(ctx, http.MethodPost, s.pods(), pod, nil); err != nil {
		return fmt.Errorf("make the step's pod: %w", err)
	}
	defer s.remove(name, 0)
	copyStderr := func() {
		if b, err := readNoFollow(errFile); err == nil {
			stderr.Write(b)
		}
	}
	// Stopping the step deletes its pod: pi gets to stop its tools first.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			grace := 0
			if spec.Graceful {
				grace = 10
			}
			s.remove(name, grace)
		case <-stopped:
		}
	}()
	bg := context.WithoutCancel(ctx)
	if _, err := s.until(ctx, name, false); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	logs, err := s.request(bg, http.MethodGet, s.pods()+"/"+name+"/log?follow=true&container=step", nil)
	if err != nil {
		return fmt.Errorf("follow the step's pod: %w", err)
	}
	_, err = io.Copy(stdout, bufio.NewReader(logs.Body))
	logs.Body.Close()
	if ctx.Err() != nil {
		copyStderr()
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("read the step's pod: %w", err)
	}
	code, err := s.until(ctx, name, true)
	copyStderr()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	if code != 0 {
		return exitError(code)
	}
	return nil
}

// Waits until the pod's container runs, or with ended until it is over,
// and returns its exit status then; fails when the pod can't start.
func (s *kubeSandbox) until(ctx context.Context, name string, ended bool) (int, error) {
	for {
		var pod struct {
			Status struct {
				Phase             string `json:"phase"`
				Reason            string `json:"reason"`
				Message           string `json:"message"`
				ContainerStatuses []struct {
					State struct {
						Waiting *struct {
							Reason  string `json:"reason"`
							Message string `json:"message"`
						} `json:"waiting"`
						Running    *struct{} `json:"running"`
						Terminated *struct {
							ExitCode int    `json:"exitCode"`
							Reason   string `json:"reason"`
						} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		}
		if err := s.call(ctx, http.MethodGet, s.pods()+"/"+name, nil, &pod); err != nil {
			return 0, fmt.Errorf("read the step's pod: %w", err)
		}
		for _, c := range pod.Status.ContainerStatuses {
			switch {
			case c.State.Terminated != nil:
				if c.State.Terminated.Reason == "OOMKilled" {
					return c.State.Terminated.ExitCode, fmt.Errorf("the step ran out of memory (SANDBOX_MEMORY_MB)")
				}
				return c.State.Terminated.ExitCode, nil
			case c.State.Running != nil && !ended:
				return 0, nil
			case c.State.Waiting != nil:
				switch c.State.Waiting.Reason {
				case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError", "CreateContainerError":
					return 0, fmt.Errorf("the step's pod can't start: %s: %s", c.State.Waiting.Reason, c.State.Waiting.Message)
				}
			}
		}
		if pod.Status.Phase == "Failed" {
			return 0, fmt.Errorf("the step's pod failed: %s %s", pod.Status.Reason, pod.Status.Message)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// The step's pod.
func (s *kubeSandbox) podSpec(spec sandboxSpec, errFile string) (map[string]any, error) {
	env := containerEnv(spec)
	if spec.Stdin != "" {
		env["STEP_STDIN"] = spec.Stdin
	}
	env["STEP_STDERR"] = errFile
	var envList []map[string]string
	for k, v := range env {
		envList = append(envList, map[string]string{"name": k, "value": v})
	}
	mounts, err := orderedMounts(spec)
	if err != nil {
		return nil, err
	}
	volumeNames := map[string]string{}
	var volumes []map[string]any
	var volumeMounts []map[string]any
	for _, m := range mounts {
		p, err := s.claimOf(m.Path)
		if err != nil {
			return nil, err
		}
		v, ok := volumeNames[p.claim]
		if !ok {
			v = fmt.Sprintf("claim-%d", len(volumeNames))
			volumeNames[p.claim] = v
			volumes = append(volumes, map[string]any{"name": v, "persistentVolumeClaim": map[string]any{"claimName": p.claim}})
		}
		volumeMounts = append(volumeMounts, map[string]any{"name": v, "mountPath": m.Path, "subPath": p.sub, "readOnly": m.ReadOnly})
	}
	volumes = append(volumes, map[string]any{"name": "tmp", "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": "2Gi"}})
	volumeMounts = append(volumeMounts, map[string]any{"name": "tmp", "mountPath": "/tmp"})
	uid, gid, _ := strings.Cut(s.user, ":")
	b := make([]byte, 6)
	rand.Read(b)
	network := "webapp"
	if spec.Offline {
		network = "offline"
	}
	podSpec := map[string]any{
		"restartPolicy":                "Never",
		"automountServiceAccountToken": false,
		"enableServiceLinks":           false,
		"securityContext": map[string]any{
			"runAsUser": json.Number(uid), "runAsGroup": json.Number(gid), "runAsNonRoot": true,
			"seccompProfile": map[string]string{"type": "RuntimeDefault"},
		},
		"containers": []map[string]any{{
			"name":            "step",
			"image":           s.image,
			"imagePullPolicy": "IfNotPresent",
			"command":         containerCmd(spec, errFile),
			"env":             envList,
			"workingDir":      spec.Dir,
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false,
				"readOnlyRootFilesystem":   true,
				"capabilities":             map[string]any{"drop": []string{"ALL"}},
			},
			"resources": map[string]any{
				"limits":   map[string]string{"memory": fmt.Sprintf("%dMi", s.memoryMB), "cpu": fmt.Sprint(s.cpus)},
				"requests": map[string]string{"memory": "256Mi", "cpu": "100m"},
			},
			"volumeMounts": volumeMounts,
		}},
		"volumes": volumes,
	}
	if s.node != "" {
		podSpec["nodeName"] = s.node
	}
	if s.runtime != "" {
		podSpec["runtimeClassName"] = s.runtime
	}
	return map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":   "spoletta-step-" + hex.EncodeToString(b),
			"labels": map[string]string{sandboxLabel: "1", sandboxWorker: labelValue(s.self), sandboxNetwork: network},
		},
		"spec": podSpec,
	}, nil
}

// Calls the cluster's API, decoding its answer into out, if any.
func (s *kubeSandbox) call(ctx context.Context, method, path string, in, out any) error {
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

// The service account's token is read for every request, as the kubelet
// rotates it.
func (s *kubeSandbox) request(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.api+path, body)
	if err != nil {
		return nil, err
	}
	token, err := os.ReadFile(s.tokenFile)
	if err != nil {
		return nil, fmt.Errorf("the service account's token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
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
		return nil, fmt.Errorf("%d %s", resp.StatusCode, e.Message)
	}
	return resp, nil
}
