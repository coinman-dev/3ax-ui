package wg

import "github.com/coinman-dev/3ax-ui/v2/shared/crypto"

// GeneratePrivateKey generates a random X25519 private key (WireGuard format).
func GeneratePrivateKey() (string, error) {
	return crypto.GeneratePrivateKey()
}

// PublicKeyFromPrivate derives an X25519 public key from a private key.
func PublicKeyFromPrivate(privKeyBase64 string) (string, error) {
	return crypto.PublicKeyFromPrivate(privKeyBase64)
}

// GenerateKeyPair generates a WireGuard private/public key pair.
func GenerateKeyPair() (privateKey, publicKey string, err error) {
	return crypto.GenerateKeyPair()
}

// GeneratePresharedKey generates a random 256-bit preshared key.
func GeneratePresharedKey() (string, error) {
	return crypto.GeneratePresharedKey()
}
