package crayd

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

var b64url = base64.RawURLEncoding

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b) // never fails on supported platforms (Go 1.24+)
	return b
}

type cookieStore struct{ aead cipher.AEAD }

type cookiePayload struct {
	Exp  int64             `json:"e"`
	Data map[string]string `json:"d"`
}

const maxCookieLen = 3800 // browsers cap a cookie at about 4 KB

// CookieSessions keeps session data in the cookie itself, encrypted with AES-256-GCM.
// No server storage is needed. secret must be at least 32 random bytes and kept private;
// changing it signs everyone out. Note: logout clears the cookie, but a copied cookie
// stays valid until it expires. Use ServerSessions when you need real revocation.
func CookieSessions(secret []byte) SessionStore {
	if len(secret) < 32 {
		panic("crayd: CookieSessions needs a secret of at least 32 bytes")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("crayd session encryption v1"))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		panic(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return &cookieStore{aead: aead}
}

func (s *cookieStore) Load(_ context.Context, v string) (map[string]string, error) {
	raw, err := b64url.DecodeString(v)
	n := s.aead.NonceSize()
	if err != nil || len(raw) < n+s.aead.Overhead() {
		return nil, nil
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	if err != nil {
		return nil, nil // tampered or wrong key
	}
	var p cookiePayload
	if json.Unmarshal(plain, &p) != nil || time.Now().Unix() > p.Exp {
		return nil, nil
	}
	if p.Data == nil {
		p.Data = map[string]string{}
	}
	return p.Data, nil
}

func (s *cookieStore) Save(_ context.Context, _ string, data map[string]string, ttl time.Duration) (string, error) {
	plain, err := json.Marshal(cookiePayload{Exp: time.Now().Add(ttl).Unix(), Data: data})
	if err != nil {
		return "", err
	}
	nonce := randomBytes(s.aead.NonceSize())
	v := b64url.EncodeToString(s.aead.Seal(nonce, nonce, plain, nil))
	if len(v) > maxCookieLen {
		return "", errors.New("crayd: session data too large for a cookie; use ServerSessions")
	}
	return v, nil
}

func (s *cookieStore) Destroy(context.Context, string) error { return nil }

// SessionBackend is a tiny key/value store with expiry (Redis, memory, ...).
type SessionBackend interface {
	Get(ctx context.Context, key string) (value []byte, found bool, err error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

type serverStore struct{ b SessionBackend }

// ServerSessions keeps data in backend under a hash of a random 256-bit ID.
// Only hashes are stored, so a leaked backend dump cannot be replayed as cookies.
func ServerSessions(backend SessionBackend) SessionStore { return &serverStore{b: backend} }

const sessionIDLen = 43 // base64url of 32 random bytes

func sessionKey(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "session:" + hex.EncodeToString(sum[:])
}

func (s *serverStore) Load(ctx context.Context, id string) (map[string]string, error) {
	if len(id) != sessionIDLen {
		return nil, nil
	}
	b, found, err := s.b.Get(ctx, sessionKey(id))
	if err != nil || !found {
		return nil, err
	}
	var data map[string]string
	if json.Unmarshal(b, &data) != nil {
		return nil, nil
	}
	if data == nil {
		data = map[string]string{}
	}
	return data, nil
}

func (s *serverStore) Save(ctx context.Context, old string, data map[string]string, ttl time.Duration) (string, error) {
	id := old
	if id == "" {
		id = b64url.EncodeToString(randomBytes(32))
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return id, s.b.Set(ctx, sessionKey(id), b, ttl)
}

func (s *serverStore) Destroy(ctx context.Context, id string) error {
	if len(id) != sessionIDLen {
		return nil
	}
	return s.b.Delete(ctx, sessionKey(id))
}

// MemorySessionBackend keeps sessions in process memory: they vanish on restart
// and are not shared between servers. Use Redis in production.
type MemorySessionBackend struct {
	mu    sync.Mutex
	items map[string]memSession
	sets  int
}

type memSession struct {
	value   []byte
	expires time.Time
}

func NewMemorySessionBackend() *MemorySessionBackend {
	return &MemorySessionBackend{items: map[string]memSession{}}
}

func (m *MemorySessionBackend) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[key]
	if !ok || time.Now().After(it.expires) {
		delete(m.items, key)
		return nil, false, nil
	}
	return append([]byte(nil), it.value...), true, nil
}

func (m *MemorySessionBackend) Set(_ context.Context, key string, v []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = memSession{value: append([]byte(nil), v...), expires: time.Now().Add(ttl)}
	if m.sets++; m.sets%1000 == 0 { // occasionally drop expired sessions
		now := time.Now()
		for k, it := range m.items {
			if now.After(it.expires) {
				delete(m.items, k)
			}
		}
	}
	return nil
}

func (m *MemorySessionBackend) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	delete(m.items, key)
	m.mu.Unlock()
	return nil
}
