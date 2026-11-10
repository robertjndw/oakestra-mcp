package actions

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const secretLen = 32

// LoadOrCreateSecret returns the HMAC key in path, creating it with fresh
// random bytes on first start. Keeping it on disk (next to the database)
// means tokens handed out before a restart stay valid until they expire.
func LoadOrCreateSecret(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) < secretLen {
			return nil, fmt.Errorf("%s holds %d bytes, want at least %d; delete it to generate a new one", path, len(b), secretLen)
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, secretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	// Write the key to a temp file first and link it into place, so another
	// replica sharing the volume never reads a created but still empty file.
	f, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return nil, err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	err = os.Link(tmp, path)
	if errors.Is(err, fs.ErrExist) {
		// Another replica won the race; use its key.
		return LoadOrCreateSecret(path)
	}
	if err != nil {
		return nil, err
	}
	return b, nil
}

func newActionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// sign binds a token to the action, the user who prepared it, the exact
// plan and the expiry. The user is part of the MAC, so a token copied to
// another account fails verification even before the stored owner is
// compared.
func sign(secret []byte, actionID, user, planHash string, expires int64) string {
	m := hmac.New(sha256.New, secret)
	// The user name is the only free-form field and could contain "|", so
	// it is length-prefixed to keep field boundaries unambiguous.
	fmt.Fprintf(m, "%s|%d:%s|%s|%d", actionID, len(user), user, planHash, expires)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func makeToken(secret []byte, actionID, user, planHash string, expires int64) string {
	return actionID + "." + strconv.FormatInt(expires, 10) + "." + sign(secret, actionID, user, planHash, expires)
}

type parsedToken struct {
	actionID string
	expires  int64
	mac      string
}

func parseToken(tok string) (parsedToken, error) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 {
		return parsedToken{}, ErrInvalidToken
	}
	if _, err := hex.DecodeString(parts[0]); err != nil || len(parts[0]) != 32 {
		return parsedToken{}, ErrInvalidToken
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return parsedToken{}, ErrInvalidToken
	}
	return parsedToken{actionID: parts[0], expires: exp, mac: parts[2]}, nil
}

func validMAC(secret []byte, t parsedToken, user, planHash string) bool {
	want := sign(secret, t.actionID, user, planHash, t.expires)
	return hmac.Equal([]byte(want), []byte(t.mac))
}
