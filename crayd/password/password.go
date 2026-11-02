// Package password hashes and checks passwords with PBKDF2-SHA256 from the
// standard library (crypto/pbkdf2, Go 1.24+). It has no HTTP code, so models can use it.
//
// Hash format (self-describing, so parameters can be raised later):
//
//	pbkdf2-sha256$<iterations>$<salt base64>$<key base64>
package password

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Iterations for new hashes. 600,000 follows OWASP's recommendation for PBKDF2-SHA256.
// Tests may lower it; NeedsRehash reports hashes made with fewer.
var Iterations = 600_000

const (
	scheme        = "pbkdf2-sha256"
	saltLen       = 16
	keyLen        = 32
	maxLen        = 1024       // longer passwords are rejected (limits work per request)
	maxIterations = 10_000_000 // refuse absurd values from a tampered database
)

var ErrTooLong = errors.New("password: too long")

var b64 = base64.RawStdEncoding

// Hash returns a new salted hash of pw.
func Hash(pw string) (string, error) {
	if len(pw) > maxLen {
		return "", ErrTooLong
	}
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, Iterations, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s$%d$%s$%s", scheme, Iterations, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Check reports whether pw matches hash. An empty or malformed hash still costs
// the same work as a real check, so "unknown user" and "wrong password" take
// the same time: call Check("", pw) when the user does not exist.
func Check(hash, pw string) bool {
	iter, salt, want, ok := parse(hash)
	if !ok || len(pw) > maxLen {
		pbkdf2.Key(sha256.New, pw, make([]byte, saltLen), Iterations, keyLen) // equal timing
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// NeedsRehash reports whether hash should be replaced (after a successful Check)
// because it uses weaker parameters than the current ones.
func NeedsRehash(hash string) bool {
	iter, _, _, ok := parse(hash)
	return !ok || iter < Iterations
}

func parse(hash string) (iter int, salt, key []byte, ok bool) {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != scheme {
		return 0, nil, nil, false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > maxIterations {
		return 0, nil, nil, false
	}
	salt, err1 := b64.DecodeString(parts[2])
	key, err2 := b64.DecodeString(parts[3])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 {
		return 0, nil, nil, false
	}
	return iter, salt, key, true
}
