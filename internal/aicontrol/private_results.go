package aicontrol

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"reflect"
	"time"

	"github.com/google/uuid"
)

type privateResult struct {
	ID         string    `json:"id"`
	Owner      string    `json:"owner"`
	Ciphertext string    `json:"ciphertext"`
	CreatedAt  time.Time `json:"createdAt"`
}

// One-time credentials returned by an API must reach the human administrator,
// without ever becoming model input, chat history or plaintext operation logs.
func HasPrivateResult(raw json.RawMessage) bool {
	var original, redacted any
	if json.Unmarshal(raw, &original) != nil || json.Unmarshal(Sanitize(raw), &redacted) != nil {
		return false
	}
	return !reflect.DeepEqual(original, redacted)
}
func (s *Store) SavePrivateResult(p Principal, raw json.RawMessage) (string, error) {
	if p.ID == "" || len(raw) > 256<<10 {
		return "", ErrInvalid
	}
	s.mu.Lock()
	a, err := s.aead(true)
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	id := uuid.NewString()
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	value := privateResult{ID: id, Owner: p.ID, CreatedAt: time.Now().UTC(), Ciphertext: base64.StdEncoding.EncodeToString(a.Seal(nonce, nonce, raw, []byte("ai-result:"+p.ID+":"+id)))}
	s.filesMu.Lock()
	defer s.filesMu.Unlock()
	items, err := s.fileRecords("private-results")
	if err != nil {
		return "", err
	}
	if len(items) >= 128 {
		return "", ErrBusy
	}
	err = s.writeFileRecord("private-results", id, value)
	return id, err
}
func (s *Store) PrivateResult(p Principal, id string) (json.RawMessage, error) {
	if p.ID == "" || !validFileID(id) {
		return nil, ErrDenied
	}
	raw, err := os.ReadFile(s.filePath("private-results", id, ".json"))
	if err != nil {
		return nil, ErrUnavailable
	}
	var v privateResult
	if json.Unmarshal(raw, &v) != nil || v.Owner != p.ID {
		return nil, ErrDenied
	}
	if time.Since(v.CreatedAt) > fileLifetime {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	a, err := s.aead(false)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	encoded, err := base64.StdEncoding.DecodeString(v.Ciphertext)
	if err != nil || len(encoded) < a.NonceSize() {
		return nil, ErrUnavailable
	}
	return a.Open(nil, encoded[:a.NonceSize()], encoded[a.NonceSize():], []byte("ai-result:"+p.ID+":"+id))
}
