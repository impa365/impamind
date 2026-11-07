package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	bundleArgonTime    = 3
	bundleArgonMemory  = 64 * 1024 // KiB
	bundleArgonThreads = 4
	bundleArgonKeyLen  = 32
	bundleSaltLen      = 16
)

func deriveBundleKey(passphrase string, salt []byte) []byte {
	return argon2.IDKey([]byte(passphrase), salt, bundleArgonTime, bundleArgonMemory, bundleArgonThreads, bundleArgonKeyLen)
}

func sealBundleVault(passphrase string, plaintext []byte) (salt, sealed []byte, err error) {
	if len(passphrase) < 8 {
		return nil, nil, fmt.Errorf("passphrase must be at least 8 characters")
	}
	salt = make([]byte, bundleSaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return nil, nil, err
	}
	key := deriveBundleKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, err
	}
	return salt, aead.Seal(nonce, nonce, plaintext, nil), nil
}

func openBundleVault(passphrase string, salt, sealed []byte) ([]byte, error) {
	if len(salt) != bundleSaltLen {
		return nil, fmt.Errorf("invalid vault.salt")
	}
	if len(passphrase) < 8 {
		return nil, fmt.Errorf("passphrase must be at least 8 characters")
	}
	key := deriveBundleKey(passphrase, salt)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	ns := aead.NonceSize()
	if len(sealed) < ns {
		return nil, fmt.Errorf("vault.sealed truncated")
	}
	plain, err := aead.Open(nil, sealed[:ns], sealed[ns:], nil)
	if err != nil {
		return nil, fmt.Errorf("wrong passphrase or corrupt vault.sealed")
	}
	return plain, nil
}

func sha256Hex(parts ...[]byte) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write(p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
