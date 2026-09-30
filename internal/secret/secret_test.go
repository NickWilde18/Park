package secret

import (
	"bytes"
	"errors"
	"testing"
)

const key = "0123456789abcdef0123456789abcdef"

// A sealed value opens with the same key and context, and hides its plaintext.
func TestSealOpen(t *testing.T) {
	box, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	sealed := box.Seal([]byte("client-secret"), "sign_in_provider:github:client_secret")
	if bytes.Contains(sealed, []byte("client-secret")) {
		t.Fatal("the sealed value contains the plaintext")
	}
	got, err := box.Open(sealed, "sign_in_provider:github:client_secret")
	if err != nil || string(got) != "client-secret" {
		t.Fatalf("got %q, %v", got, err)
	}
	if again := box.Seal([]byte("client-secret"), "sign_in_provider:github:client_secret"); bytes.Equal(again, sealed) {
		t.Fatal("two seals of one value are equal: the nonce is not fresh")
	}
}

// A value does not open for another context, with another key, or when corrupted.
func TestOpenRefuses(t *testing.T) {
	box, _ := New(key)
	other, _ := New("fedcba9876543210fedcba9876543210")
	sealed := box.Seal([]byte("client-secret"), "sign_in_provider:github:client_secret")
	corrupt := append([]byte{}, sealed...)
	corrupt[len(corrupt)-1] ^= 1
	for name, open := range map[string]func() ([]byte, error){
		"another context": func() ([]byte, error) { return box.Open(sealed, "sign_in_provider:other:client_secret") },
		"another key":     func() ([]byte, error) { return other.Open(sealed, "sign_in_provider:github:client_secret") },
		"corrupted":       func() ([]byte, error) { return box.Open(corrupt, "sign_in_provider:github:client_secret") },
		"too short":       func() ([]byte, error) { return box.Open(sealed[:4], "sign_in_provider:github:client_secret") },
	} {
		if _, err := open(); !errors.Is(err, ErrCannotOpen) {
			t.Errorf("%s: got %v", name, err)
		}
	}
}
