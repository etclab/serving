package mutil

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"log"
	"sync"

	"github.com/etclab/pre"
)

const KeySize = 32
const NonceSize = 12

func LogWithPrefix(prefix string) func(format string, v ...interface{}) {
	return func(format string, v ...interface{}) {
		log.Printf("["+prefix+"] "+format, v...)
	}
}

type PreKeys interface {
	pre.PublicKey | pre.PublicParams | pre.ReEncryptionKey | pre.KeyPair
}

func GSafeWriteToMap[K comparable, V PreKeys](key K, value *V, gMap *map[K]*V, lock *sync.RWMutex) *V {
	lock.Lock()
	defer lock.Unlock()
	if *gMap == nil {
		*gMap = make(map[K]*V)
	}
	(*gMap)[key] = value
	return value
}

func GSafeReadFromMap[K comparable, V PreKeys](key K, gMap map[K]*V, lock *sync.RWMutex) *V {
	lock.RLock()
	defer lock.RUnlock()
	return gMap[key]
}

// placeholder encryption
func FakeEncrypt(plaintext []byte) ([]byte, error) {
	return bytes.ToUpper(plaintext), nil
}

// placeholder decryption
func FakeDecrypt(encBody []byte) ([]byte, error) {
	return bytes.ToLower(encBody), nil
}

func NewAESGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes.NewCipher failed: %v", err)
	}

	aesgcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cipher.NewGCM failed: %v", err)
	}

	return aesgcm, nil
}

func AESGCMEncrypt(key, plaintext []byte) ([]byte, error) {
	aesgcm, err := NewAESGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize) // zero nonce
	return aesgcm.Seal(plaintext[:0], nonce, plaintext, nil), nil
}

func AESGCMDecrypt(key, ciphertext []byte) ([]byte, error) {
	aesgcm, err := NewAESGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, NonceSize) // zero nonce
	return aesgcm.Open(nil, nonce, ciphertext, nil)
}
