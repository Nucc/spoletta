package main

// Secrets the webapp keeps for others, the model keys admins save and the
// tokens of people's MCP connections, are sealed in the database with
// SECRET_KEY, so a copy of the database, a backup or a dump doesn't hand
// them out. Each is sealed with AES-256-GCM under a key derived from
// SECRET_KEY, and bound to where it is kept, its table, column and row, so
// one can't be moved to stand in for another.
//
// Without SECRET_KEY secrets are kept as they come, as before there was
// sealing, with a warning. Once it is set, starting seals every secret still
// kept as it came, and opens the sealed ones, so a webapp started with a
// different SECRET_KEY, or without one, stops at once instead of failing
// each agent step.
//
// The key is rotated through SECRET_KEY_SECONDARY, the key coming in: a
// webapp with it opens secrets sealed with either key and seals new ones
// with the secondary. Starting leaves those sealed with the primary as they
// are, as webapps still without the secondary go on reading them while the
// others start; `webapp seal-secrets`, run once every webapp has the
// secondary, seals them again with it and exits. Then the secondary becomes
// SECRET_KEY, and SECRET_KEY_SECONDARY goes; see docs/SECRETS.md.
//
// Either key can instead be in a file that SECRET_KEY_FILE or
// SECRET_KEY_SECONDARY_FILE names, as secret stores that mount secrets as
// files hand them over.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// What a sealed secret starts with; one kept as it came doesn't.
const sealedPrefix = "sealed:v1:"

// The shortest key taken, such as `openssl rand -base64 32` makes.
const minSecretKey = 32

// Seals and opens secrets; nil without SECRET_KEY.
type sealer struct {
	// Seals with the first, the secondary key while rotating; opens with
	// either.
	keys []cipher.AEAD
}

// The webapp's sealer, set up in main.
var secrets *sealer

// The key in the variable name, or in the file name_FILE names, with the
// line end a file has left off; empty when neither is set.
func secretKeyFromEnv(name string) (string, error) {
	path := os.Getenv(name + "_FILE")
	if path == "" {
		return os.Getenv(name), nil
	}
	if os.Getenv(name) != "" {
		return "", fmt.Errorf("%s and %s_FILE are both set; set one", name, name)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

// A sealer with the primary key and, while rotating, the secondary.
func newSealer(primary, secondary string) (*sealer, error) {
	if primary == "" {
		if secondary != "" {
			return nil, errors.New("SECRET_KEY_SECONDARY is set without SECRET_KEY; to start sealing, set SECRET_KEY")
		}
		return nil, nil
	}
	s := &sealer{}
	for _, k := range []struct{ name, key string }{{"SECRET_KEY_SECONDARY", secondary}, {"SECRET_KEY", primary}} {
		if k.key == "" {
			continue
		}
		if len(k.key) < minSecretKey {
			return nil, fmt.Errorf("%s must be at least %d characters, such as `openssl rand -base64 32` makes", k.name, minSecretKey)
		}
		key, err := hkdf.Key(sha256.New, []byte(k.key), nil, "spoletta secrets", 32)
		if err != nil {
			return nil, err
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		s.keys = append(s.keys, aead)
	}
	return s, nil
}

func sealed(stored string) bool {
	return strings.HasPrefix(stored, sealedPrefix)
}

// The secret as it is kept at where: sealed, or as it came without SECRET_KEY.
func (s *sealer) seal(where, secret string) (string, error) {
	if s == nil {
		return secret, nil
	}
	aead := s.keys[0]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	box := aead.Seal(nonce, nonce, []byte(secret), []byte(where))
	return sealedPrefix + base64.RawStdEncoding.EncodeToString(box), nil
}

var errSealed = errors.New("this secret is sealed, and SECRET_KEY isn't set")

// The secret kept at where, opened if sealed.
func (s *sealer) open(where, stored string) (string, error) {
	secret, _, err := s.openAged(where, stored)
	return secret, err
}

// The secret kept at where, opened if sealed, and whether it was sealed
// with the key the sealer seals with now.
func (s *sealer) openAged(where, stored string) (string, bool, error) {
	if !sealed(stored) {
		return stored, false, nil
	}
	if s == nil {
		return "", false, errSealed
	}
	box, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(stored, sealedPrefix))
	for i, aead := range s.keys {
		n := aead.NonceSize()
		if err != nil || len(box) < n {
			return "", false, errors.New("this sealed secret is damaged")
		}
		if secret, err := aead.Open(nil, box[:n], box[n:], []byte(where)); err == nil {
			return string(secret), i == 0, nil
		}
	}
	return "", false, errors.New("SECRET_KEY doesn't open this secret; it was sealed with another")
}

// Where a model key is kept, and an MCP connection's token, which column
// says.
func modelKeyPlace(env string) string { return "model_keys/" + env }

// Where a model provider's key is kept.
func modelProviderPlace(id string) string { return "model_providers/" + id }

func mcpTokenPlace(column string, server, user int64) string {
	return fmt.Sprintf("mcp_tokens/%s/%d/%d", column, server, user)
}

// Sets up the webapp's sealer from SECRET_KEY and SECRET_KEY_SECONDARY,
// seals the secrets kept as they came, with reseal those sealed with the
// key going out too, and checks it opens every one. Runs after the tables'
// migrations. Says how many it left as they were since they changed
// meanwhile, which another webapp sealed, perhaps with the key going out.
func setUpSecrets(db *sql.DB, reseal bool) (int, error) {
	primary, err := secretKeyFromEnv("SECRET_KEY")
	if err != nil {
		return 0, err
	}
	secondary, err := secretKeyFromEnv("SECRET_KEY_SECONDARY")
	if err != nil {
		return 0, err
	}
	rotating := secondary != ""
	s, err := newSealer(primary, secondary)
	if err != nil {
		return 0, err
	}
	secrets = s
	if s == nil {
		log.Printf("WARNING: SECRET_KEY isn't set, so model keys and MCP tokens are kept in the database unsealed")
	}
	ctx := context.Background()
	// A secret, and how to seal it where it is unless it changed meanwhile.
	type secret struct {
		update, where, stored string
		args                  []any
	}
	var all []secret
	err = scanAll(ctx, db, "SELECT env, api_key FROM model_keys", func(rows *sql.Rows) error {
		var env, key string
		if err := rows.Scan(&env, &key); err != nil {
			return err
		}
		all = append(all, secret{"UPDATE model_keys SET api_key = ? WHERE env = ? AND api_key = ?", modelKeyPlace(env), key, []any{env, key}})
		return nil
	})
	if err != nil {
		return 0, err
	}
	err = scanAll(ctx, db, "SELECT id, api_key FROM model_providers WHERE api_key <> ''", func(rows *sql.Rows) error {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return err
		}
		all = append(all, secret{"UPDATE model_providers SET api_key = ? WHERE id = ? AND api_key = ?", modelProviderPlace(id), key, []any{id, key}})
		return nil
	})
	if err != nil {
		return 0, err
	}
	err = scanAll(ctx, db, "SELECT server_id, user_id, access_token, refresh_token FROM mcp_tokens", func(rows *sql.Rows) error {
		var server, user int64
		var access, refresh sql.NullString
		if err := rows.Scan(&server, &user, &access, &refresh); err != nil {
			return err
		}
		for column, token := range map[string]sql.NullString{"access_token": access, "refresh_token": refresh} {
			if token.Valid {
				all = append(all, secret{"UPDATE mcp_tokens SET " + column + " = ? WHERE server_id = ? AND user_id = ? AND " + column + " = ?",
					mcpTokenPlace(column, server, user), token.String, []any{server, user, token.String}})
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	unsealed, aged, changed := 0, 0, 0
	for _, sec := range all {
		secret, current, err := s.openAged(sec.where, sec.stored)
		if err != nil {
			return 0, fmt.Errorf("the secret at %s: %w", sec.where, err)
		}
		if s == nil || current || (sealed(sec.stored) && !reseal) {
			continue
		}
		if sealed(sec.stored) {
			aged++
		} else {
			unsealed++
		}
		v, err := s.seal(sec.where, secret)
		if err != nil {
			return 0, err
		}
		res, err := db.ExecContext(ctx, sec.update, append([]any{v}, sec.args...)...)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if n == 0 {
			changed++
		}
	}
	if unsealed > 0 {
		log.Printf("sealed %d secrets kept unsealed before SECRET_KEY was set", unsealed)
	}
	if aged > 0 {
		log.Printf("sealed %d secrets again with SECRET_KEY_SECONDARY", aged)
	}
	switch {
	case changed > 0:
		log.Printf("%d secrets changed while being sealed; seal them again with `webapp seal-secrets`", changed)
	case rotating && reseal:
		log.Printf("all %d secrets are sealed with SECRET_KEY_SECONDARY", len(all))
	}
	return changed, nil
}
