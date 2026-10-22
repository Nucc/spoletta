package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// A cluster's API holding the worker's pod, which runs the pods it is
// given as a kubelet would, printing what their command says: "part" for
// the probe of subpaths, which it gives what it asks for, or hello and an
// error on stderr, exiting 3.
type fakeCluster struct {
	mu      sync.Mutex
	pods    map[string]map[string]any
	logged  map[string]bool
	deleted []string
	made    []map[string]any
}

func (c *fakeCluster) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer sa-token" {
		http.Error(w, `{"message": "Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	const base = "/api/v1/namespaces/flows/pods"
	name := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, base), "/")
	switch {
	case r.Method == "GET" && name == "worker-0":
		json.NewEncoder(w).Encode(map[string]any{"spec": map[string]any{
			"nodeName": "node-a",
			"containers": []any{map[string]any{"volumeMounts": []any{
				map[string]any{"name": "data", "mountPath": filesRoot, "subPath": "files"},
				map[string]any{"name": "data", "mountPath": workspacesRoot, "subPath": "workspaces"},
				map[string]any{"name": "steps", "mountPath": stepsRoot},
				map[string]any{"name": "token", "mountPath": "/var/run/secrets/x"},
			}}},
			"volumes": []any{
				map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": "spoletta-data"}},
				map[string]any{"name": "steps", "persistentVolumeClaim": map[string]any{"claimName": "spoletta-steps"}},
				map[string]any{"name": "token", "projected": map[string]any{}},
			},
		}})
	case r.Method == "GET" && name == "":
		json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
	case r.Method == "POST" && name == "":
		var pod map[string]any
		json.NewDecoder(r.Body).Decode(&pod)
		n := pod["metadata"].(map[string]any)["name"].(string)
		c.pods[n] = pod
		c.made = append(c.made, pod)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("{}"))
	case r.Method == "GET" && strings.HasSuffix(name, "/log"):
		n := strings.TrimSuffix(name, "/log")
		c.logged[n] = true
		container := c.pods[n]["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		cmd := fmt.Sprint(container["command"])
		var stderrFile string
		for _, e := range container["env"].([]any) {
			if e.(map[string]any)["name"] == "STEP_STDERR" {
				stderrFile = e.(map[string]any)["value"].(string)
			}
		}
		if strings.Contains(cmd, "echo part") {
			w.Write([]byte("part\n"))
			return
		}
		os.WriteFile(stderrFile, []byte("oops\n"), 0o600)
		w.Write([]byte("hello\n"))
	case r.Method == "GET":
		state := map[string]any{"running": map[string]any{}}
		if c.logged[name] {
			code := 0
			if !strings.Contains(fmt.Sprint(c.pods[name]["spec"]), "echo part") {
				code = 3
			}
			state = map[string]any{"terminated": map[string]any{"exitCode": code}}
		}
		json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"state": state}}}})
	case r.Method == "DELETE":
		c.deleted = append(c.deleted, name)
		w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func TestKubernetesSandbox(t *testing.T) {
	cluster := &fakeCluster{pods: map[string]map[string]any{}, logged: map[string]bool{}}
	srv := httptest.NewTLSServer(cluster)
	defer srv.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600)
	os.WriteFile(filepath.Join(dir, "token"), []byte("sa-token\n"), 0o600)
	stepsRoot, workspacesRoot, filesRoot = filepath.Join(dir, "steps"), filepath.Join(dir, "workspaces"), filepath.Join(dir, "files")
	t.Cleanup(func() { stepsRoot, workspacesRoot, filesRoot = os.TempDir(), "/data/workspaces", "/data/files" })
	for k, v := range map[string]string{
		"KUBE_API_URL": srv.URL, "KUBE_CA_FILE": ca, "KUBE_TOKEN_FILE": filepath.Join(dir, "token"),
		"SANDBOX_NAMESPACE": "flows", "POD_NAME": "worker-0", "SANDBOX_IMAGE": "registry.example/spoletta-sandbox:1",
		"SANDBOX_USER": fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "SANDBOX_RUNTIME": "gvisor",
	} {
		t.Setenv(k, v)
	}
	s, err := newKubeSandbox(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cluster.made) != 1 {
		t.Fatalf("the probe made %d pods", len(cluster.made))
	}

	ws := filepath.Join(workspacesRoot, "run-3")
	os.MkdirAll(ws, 0o700)
	private, _ := makeStepDir("private-")
	os.WriteFile(filepath.Join(private, "prompt"), []byte("hi"), 0o600)
	var out, errOut bytes.Buffer
	err = s.run(context.Background(), sandboxSpec{
		Cmd: []string{"pi", "-p"}, Stdin: filepath.Join(private, "prompt"), Dir: ws, Env: []string{"A=1", "PATH=/worker/bin"},
		Mounts: []mount{{Path: ws}, {Path: private, ReadOnly: true}}, Egress: "http://step:tok@webapp:8080",
	}, &out, &errOut)
	var exit exitError
	if !errors.As(err, &exit) || exit != 3 || out.String() != "hello\n" || errOut.String() != "oops\n" {
		t.Errorf("ran with %v, printing %q and %q", err, out.String(), errOut.String())
	}
	pod := cluster.made[1]
	spec := pod["spec"].(map[string]any)
	b, _ := json.Marshal(pod)
	for _, want := range []string{
		`"automountServiceAccountToken":false`, `"enableServiceLinks":false`, `"nodeName":"node-a"`, `"runtimeClassName":"gvisor"`,
		`"runAsNonRoot":true`, `"readOnlyRootFilesystem":true`, `"allowPrivilegeEscalation":false`, `"drop":["ALL"]`,
		`"restartPolicy":"Never"`, `"spoletta.sandbox/network":"webapp"`, `"image":"registry.example/spoletta-sandbox:1"`,
		`"mountPath":"` + ws + `","name":"claim-0","readOnly":false,"subPath":"workspaces/run-3"`,
		`"mountPath":"` + private + `","name":"claim-1","readOnly":true,"subPath":"` + filepath.Base(private) + `"`,
		`"claimName":"spoletta-data"`, `"claimName":"spoletta-steps"`,
		`"name":"HTTPS_PROXY","value":"http://step:tok@webapp:8080"`, `"name":"HOME","value":"/tmp"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the pod lacks %s: %s", want, b)
		}
	}
	if strings.Contains(string(b), "/worker/bin") || len(spec["volumes"].([]any)) != 3 {
		t.Errorf("the pod: %s", b)
	}
	probe, _ := json.Marshal(cluster.made[0])
	if !strings.Contains(string(probe), `"spoletta.sandbox/network":"offline"`) {
		t.Errorf("the probe's pod wasn't offline: %s", probe)
	}
	if len(cluster.deleted) < 2 {
		t.Errorf("deleted %v, want both pods", cluster.deleted)
	}
}
