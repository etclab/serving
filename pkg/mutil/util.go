package mutil

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"sync"

	bls "github.com/cloudflare/circl/ecc/bls12381"
	"github.com/etclab/pre"
	"knative.dev/serving/pkg/samba"
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

func Decrypt(pp *pre.PublicParams, sk any, m *samba.SambaMessage) ([]byte, error) {
	skPRE, ok := sk.(*pre.SecretKey)
	if !ok {
		return nil, fmt.Errorf("pk is not a proxy re-encryption SecretKey")
	}

	var gt *bls.Gt

	if m.IsReEncrypted {
		ct2, err := m.WrappedKey2.DeSerialize()
		if err != nil {
			return nil, err
		}
		gt = pre.Decrypt2(pp, ct2, skPRE)
	} else {
		ct1, err := m.WrappedKey1.DeSerialize()
		if err != nil {
			return nil, err
		}
		gt = pre.Decrypt1(pp, ct1, skPRE)
	}

	key := pre.KdfGtToAes256(gt)
	plaintext, err := AESGCMDecrypt(key, m.Ciphertext)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

func ReEncrypt(pp *pre.PublicParams, rk *pre.ReEncryptionKey, m *samba.SambaMessage) (*samba.SambaMessage, error) {
	ct1, err := m.WrappedKey1.DeSerialize()
	if err != nil {
		return nil, err
	}

	ct2 := pre.ReEncrypt(pp, rk, ct1)

	var wk2 samba.Ciphertext2Serialized
	err = wk2.Serialize(ct2)
	if err != nil {
		return nil, err
	}

	return &samba.SambaMessage{
		Target:        m.Target,
		IsReEncrypted: true,
		WrappedKey2:   wk2,
		Ciphertext:    m.Ciphertext,
	}, nil
}

// plainBytes is encrypted with (pp & pk - public params & public key)
// of the target service
func PreEncrypt(pp *pre.PublicParams, pk *pre.PublicKey, plainBytes []byte,
	target string) (encryptedBytes []byte, err error) {
	m := pre.RandomGt()
	wrappedKey := pre.Encrypt(pp, m, pk)
	key := pre.KdfGtToAes256(m)

	cipherText, err := AESGCMEncrypt(key, plainBytes)
	if err != nil {
		return nil, fmt.Errorf("AESGCMEncrypt failed: %v", err)
	}

	var wrappedKeySerialized samba.Ciphertext1Serialized
	err = wrappedKeySerialized.Serialize(wrappedKey)
	if err != nil {
		return nil, fmt.Errorf("wrappedKey serialization failed: %v", err)
	}

	sambaMessage := &samba.SambaMessage{
		// not used right now
		Target: target,
		// always false because the message is always encrypted for leader's public key
		// the member's proxy needs to re-encrypt it for the member's public key
		IsReEncrypted: false,
		WrappedKey1:   wrappedKeySerialized,
		Ciphertext:    cipherText,
	}

	encryptedBytes, err = json.Marshal(sambaMessage)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal failed: %v", err)
	}

	return encryptedBytes, nil
}

// these methods are copied from cryptofun repo
// TODO: make cryptofun public and import them from there
func UnmarshalRSAPrivateKeyFromPEM(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return nil, errors.New("error: failed fo parse PEM block containing private key")
	}

	priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}

	sk, ok := priv.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("error: file does not contain an RSA private key")
	}

	return sk, nil
}

func MarshalRSAPrivateKeyToPEM(sk *rsa.PrivateKey) ([]byte, error) {
	derData, err := x509.MarshalPKCS8PrivateKey(sk)
	if err != nil {
		return nil, err
	}

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: derData,
	}

	pemData := pem.EncodeToMemory(block)
	if pemData == nil {
		return nil, err
	}

	return pemData, nil
}

func RSAEncrypt(pk *rsa.PublicKey, msg []byte) ([]byte, error) {
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pk, msg, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to RSA encrypt: %v", err)
	}
	return ciphertext, nil
}

func RSADecrypt(sk *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	return rsa.DecryptOAEP(sha256.New(), rand.Reader, sk, ciphertext, nil)
}
