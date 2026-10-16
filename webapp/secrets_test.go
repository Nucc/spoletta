package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecretKey = "0123456789abcdef0123456789abcdef"

// A sealed secret opens where it was sealed, with the key it was sealed
// with, and nowhere else.
func TestSealer(t *testing.T) {
	s, err := newSealer(testSecretKey, "")
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.seal(modelKeyPlace("OPENAI_API_KEY"), "sk-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !sealed(box) || strings.Contains(box, "sk-secret") {
		t.Fatalf("sealed as %q", box)
	}
	if again, _ := s.seal(modelKeyPlace("OPENAI_API_KEY"), "sk-secret"); again == box {
		t.Errorf("sealing twice gave the same %q", box)
	}
	if got, err := s.open(modelKeyPlace("OPENAI_API_KEY"), box); err != nil || got != "sk-secret" {
		t.Errorf("open = %q, %v", got, err)
	}
	if _, err := s.open(modelKeyPlace("GATEWAY_KEY"), box); err == nil {
		t.Errorf("opened somewhere else")
	}
	other, _ := newSealer(strings.Repeat("x", minSecretKey), "")
	if _, err := other.open(modelKeyPlace("OPENAI_API_KEY"), box); err == nil {
		t.Errorf("opened with another key")
	}
	var none *sealer
	if _, err := none.open(modelKeyPlace("OPENAI_API_KEY"), box); err != errSealed {
		t.Errorf("opened without a key: %v", err)
	}
	if _, err := s.open(modelKeyPlace("OPENAI_API_KEY"), sealedPrefix+"!!"); err == nil {
		t.Errorf("opened a damaged one")
	}
}

// Without a key secrets are kept as they come, and one kept so opens as it is.
func TestSealerWithoutKey(t *testing.T) {
	s, err := newSealer("", "")
	if s != nil || err != nil {
		t.Fatalf("newSealer(\"\") = %v, %v", s, err)
	}
	if got, _ := s.seal("x", "plain"); got != "plain" {
		t.Errorf("seal without a key = %q", got)
	}
	k, _ := newSealer(testSecretKey, "")
	if got, err := k.open("x", "plain"); got != "plain" || err != nil {
		t.Errorf("open of an unsealed one = %q, %v", got, err)
	}
	if _, err := newSealer("short", ""); err == nil {
		t.Errorf("took a short key")
	}
}

// While rotating, a sealer opens secrets sealed with either key, says which
// were sealed with the primary, and seals with the secondary.
func TestSealerRotating(t *testing.T) {
	secondary := strings.Repeat("s", minSecretKey)
	old, _ := newSealer(testSecretKey, "")
	next, _ := newSealer(secondary, "")
	both, err := newSealer(testSecretKey, secondary)
	if err != nil {
		t.Fatal(err)
	}
	where := modelKeyPlace("OPENAI_API_KEY")
	aged, _ := old.seal(where, "sk-old")
	if got, current, err := both.openAged(where, aged); got != "sk-old" || current || err != nil {
		t.Errorf("one sealed with the primary = %q, %v, %v", got, current, err)
	}
	box, _ := both.seal(where, "sk-new")
	if got, current, err := both.openAged(where, box); got != "sk-new" || !current || err != nil {
		t.Errorf("one sealed while rotating = %q, %v, %v", got, current, err)
	}
	if got, err := next.open(where, box); got != "sk-new" || err != nil {
		t.Errorf("the secondary alone opened %q, %v", got, err)
	}
	if _, err := old.open(where, box); err == nil {
		t.Errorf("the primary alone opened one sealed while rotating")
	}
	if _, err := newSealer("", secondary); err == nil {
		t.Errorf("took a secondary without a primary")
	}
	if _, err := newSealer(testSecretKey, "short"); err == nil || !strings.Contains(err.Error(), "SECRET_KEY_SECONDARY") {
		t.Errorf("a short secondary: %v", err)
	}
}

// A key comes from its variable, or from the file its _FILE variable names,
// without the file's line end; not from both.
func TestSecretKeyFromEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(testSecretKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		value, file, want string
		fails             bool
	}{
		{"", "", "", false},
		{testSecretKey, "", testSecretKey, false},
		{"", path, testSecretKey, false},
		{testSecretKey, path, "", true},
		{"", filepath.Join(t.TempDir(), "missing"), "", true},
	} {
		t.Setenv("SPOLETTA_TEST_KEY", c.value)
		t.Setenv("SPOLETTA_TEST_KEY_FILE", c.file)
		got, err := secretKeyFromEnv("SPOLETTA_TEST_KEY")
		if got != c.want || (err != nil) != c.fails {
			t.Errorf("with %q and file %q: %q, %v", c.value, c.file, got, err)
		}
	}
}
