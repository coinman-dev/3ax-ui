package crypto

import (
	"crypto/rand"
	"encoding/base64"

	"golang.org/x/crypto/curve25519"
)

// GeneratePrivateKey generates a random X25519 private key (WireGuard format).
func GeneratePrivateKey() (string, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	// Clamp the key per X25519/WireGuard spec
	key[0] &= 248
	key[31] = (key[31] & 127) | 64
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

// PublicKeyFromPrivate derives an X25519 public key from a private key.
func PublicKeyFromPrivate(privKeyBase64 string) (string, error) {
	privBytes, err := base64.StdEncoding.DecodeString(privKeyBase64)
	if err != nil {
		return "", err
	}
	pubBytes, err := curve25519.X25519(privBytes, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pubBytes), nil
}

// GenerateKeyPair generates a WireGuard private/public key pair.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	privateKey, err = GeneratePrivateKey()
	if err != nil {
		return
	}
	publicKey, err = PublicKeyFromPrivate(privateKey)
	return
}

// GeneratePresharedKey generates a random 256-bit preshared key.
func GeneratePresharedKey() (string, error) {
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}
