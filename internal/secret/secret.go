// Package secret seals the secrets Park stores — sign-in provider credentials, forge credentials, webhook secrets
// — with a key derived from the server's secret key. A sealed value cannot be read without that key, and opens
// only with the context it was sealed for, so that a value copied into another row or column does not open there.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
)

// Box seals and opens values.
type Box struct {
	aead cipher.AEAD
}

// ErrCannotOpen reports a sealed value that is corrupt, sealed with another key, or sealed for another context.
var ErrCannotOpen = errors.New("the sealed value cannot be opened")

// New derives the sealing key from the server's secret key.
func New(serverKey string) (*Box, error) {
	key, err := hkdf.Key(sha256.New, []byte(serverKey), nil, "park secret box v1", 32)
	if err != nil {
		return nil, fmt.Errorf("derive the sealing key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext for context, such as "sign_in_provider:github:client_secret", with a fresh nonce.
func (b *Box) Seal(plaintext []byte, context string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, []byte(context))
}

// Open decrypts a value that Seal sealed for context.
func (b *Box) Open(sealed []byte, context string) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrCannotOpen
	}
	plaintext, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(context))
	if err != nil {
		return nil, ErrCannotOpen
	}
	return plaintext, nil
}
