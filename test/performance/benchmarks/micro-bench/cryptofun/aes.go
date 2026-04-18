package cryptofun

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"io"

	"github.com/etclab/mu"
)

const AESKeySize = 32
const AESGCMNonceSize = 12

func GenerateAESKey() []byte {
	key := make([]byte, AESKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		mu.Panicf("can't randomly generate AES key: %v", err)
	}
	return key
}

func NewAESGCM(key []byte) cipher.AEAD {
	block, err := aes.NewCipher(key)
	if err != nil {
		mu.Panicf("aes.NewCipher failed: %v", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		mu.Panicf("cipher.NewGCM failed: %v", err)
	}

	return aesgcm
}

func AESGCMEncrypt(key, plaintext []byte) []byte {
	aesgcm := NewAESGCM(key)
	nonce := make([]byte, AESGCMNonceSize) // zero nonce
	return aesgcm.Seal(plaintext[:0], nonce, plaintext, nil)
}

func AESGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	aesgcm := NewAESGCM(key)
	nonce := make([]byte, AESGCMNonceSize) // zero nonce
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}
