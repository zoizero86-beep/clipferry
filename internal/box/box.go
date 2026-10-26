// Package box holds the small amount of cryptography Clipferry needs: an
// authenticated cipher for clips and the derivation of relay room names.
// Everything a relay sees has passed through Seal.
package box

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize is the size of a ferry key and of pairing session keys.
const KeySize = 32

// NewKey returns a fresh random key.
func NewKey() []byte {
	k := make([]byte, KeySize)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	return k
}

// Seal encrypts and authenticates plaintext with XChaCha20-Poly1305.
// The random nonce is prepended to the ciphertext.
func Seal(key, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize(), aead.NonceSize()+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nonce, nonce, plaintext, nil), nil
}

// ErrOpen is returned when a message was not sealed with the given key or
// was tampered with.
var ErrOpen = errors.New("box: message authentication failed")

// Open reverses Seal.
func Open(key, sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrOpen
	}
	out, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], nil)
	if err != nil {
		return nil, ErrOpen
	}
	return out, nil
}

// Derive returns a 32-byte key bound to a purpose label.
func Derive(key []byte, label string) []byte {
	h := sha256.New()
	h.Write([]byte("clipferry/v1/" + label + "\x00"))
	h.Write(key)
	return h.Sum(nil)
}

// RoomID is the relay room a ferry's devices meet in. It is derived from the
// ferry key, so the relay never learns the key itself.
func RoomID(ferryKey []byte) string {
	return hex.EncodeToString(Derive(ferryKey, "room")[:16])
}
