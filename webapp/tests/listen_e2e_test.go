package tests

// Without sign-in anyone who reaches the webapp can do anything in it, so it
// then listens only on this machine, unless HOST says where.

import (
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

// An address of this machine other than its loopback, if it has one.
func outsideAddress(t *testing.T) string {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && !n.IP.IsLoopback() && n.IP.To4() != nil {
			return n.IP.String()
		}
	}
	t.Skip("this machine has no address but its loopback")
	return ""
}

// Whether the webapp at base answers on host instead.
func answersOn(t *testing.T, base, host string) bool {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, u.Port()) + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

func TestListenWithoutSignInOnlyOnThisMachine(t *testing.T) {
	outside := outsideAddress(t)
	a := newApp(t)
	if code, _ := a.do("GET", "/health", nil); code != http.StatusOK {
		t.Fatalf("on the loopback: %d", code)
	}
	if answersOn(t, a.url, outside) {
		t.Errorf("a webapp without sign-in answered on %s", outside)
	}
}

func TestListenWithoutSignInWhereHostSays(t *testing.T) {
	outside := outsideAddress(t)
	a := startApp(t, newFakeWorker(t).url, "HOST=0.0.0.0")
	if !answersOn(t, a.url, outside) {
		t.Errorf("a webapp with HOST=0.0.0.0 didn't answer on %s", outside)
	}
}

func TestListenWithSignInEverywhere(t *testing.T) {
	outside := outsideAddress(t)
	a, _ := usersApp(t)
	if !answersOn(t, a.url, outside) {
		t.Errorf("a webapp people sign in to didn't answer on %s", outside)
	}
}
