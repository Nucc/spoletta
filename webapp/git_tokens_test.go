package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

func testAppKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

// The app's token is asked for with a JWT signed with its key, for the
// run's repositories by name, which must all be on its GitHub; otherwise
// the static token is handed out as it is.
func TestGitHubAppTokens(t *testing.T) {
	key, pemKey := testAppKey(t)
	var asked struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/app/installations/77/access_tokens" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
		var c struct {
			Iss      string `json:"iss"`
			Iat, Exp int64
		}
		json.Unmarshal(claims, &c)
		if rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, sum[:], sig) != nil || c.Iss != "12" || c.Exp-c.Iat > 600 {
			http.Error(w, `{"message": "bad JWT"}`, http.StatusUnauthorized)
			return
		}
		json.NewDecoder(r.Body).Decode(&asked)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token": "ghs_app", "expires_at": "2030-01-01T00:00:00Z"}`))
	}))
	defer github.Close()
	t.Setenv("GITHUB_APP_ID", "12")
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "77")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", pemKey)
	t.Setenv("GITHUB_API_URL", github.URL)
	t.Setenv("GIT_TOKEN", "static")
	s, err := newGitTokenSource()
	if err != nil {
		t.Fatal(err)
	}
	// The test's GitHub is at its own host, as an Enterprise's would be.
	host := strings.TrimPrefix(github.URL, "http://")
	c, err := s.token(context.Background(), []string{"https://" + host + "/acme/api.git", "https://" + host + "/acme/web"})
	if err != nil || c.Token != "ghs_app" || c.Expires == nil || !c.Expires.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("app token = %+v, %v", c, err)
	}
	if !slices.Equal(asked.Repositories, []string{"api", "web"}) || asked.Permissions["contents"] != "write" {
		t.Errorf("asked for %+v", asked)
	}
	c, err = s.token(context.Background(), []string{"https://" + host + "/acme/api", "https://gitlab.example.com/acme/lib"})
	if err != nil || c.Token != "static" || c.Expires != nil {
		t.Errorf("for repositories elsewhere too = %+v, %v", c, err)
	}

	t.Setenv("GITHUB_APP_ID", "")
	if _, err := newGitTokenSource(); err == nil {
		t.Error("an app without its ID was taken")
	}
	t.Setenv("GITHUB_APP_INSTALLATION_ID", "")
	t.Setenv("GITHUB_APP_PRIVATE_KEY", "")
	t.Setenv("GIT_TOKEN", "")
	if s, err := newGitTokenSource(); err != nil || s.app != nil {
		t.Errorf("without anything = %+v, %v", s, err)
	} else if c, err := s.token(context.Background(), []string{"https://github.com/acme/api"}); c != nil || err != nil {
		t.Errorf("without a token = %+v, %v", c, err)
	}
}
