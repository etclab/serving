package cryptofun

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"

	"github.com/etclab/mu"
)

func GenerateRSAKeyPair(numBits int) (*rsa.PublicKey, *rsa.PrivateKey) {
	sk, err := rsa.GenerateKey(rand.Reader, numBits)
	if err != nil {
		mu.BUG("Error generating RSA key: %v", err)
	}

	sk.Precompute()

	tmp := sk.Public()
	pk := tmp.(*rsa.PublicKey)

	return pk, sk
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

func StoreRSAPrivateKeyToPEMFile(sk *rsa.PrivateKey, keyPath string) error {
	pemData, err := MarshalRSAPrivateKeyToPEM(sk)
	if err != nil {
		return err
	}
	return os.WriteFile(keyPath, pemData, 0o644)
}

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

func LoadRSAPrivateKeyFromPEMFile(keyPath string) (*rsa.PrivateKey, error) {
	pemData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	return UnmarshalRSAPrivateKeyFromPEM(pemData)
}

func RSASignSHA256(sk *rsa.PrivateKey, digest []byte) ([]byte, error) {
	return rsa.SignPSS(rand.Reader, sk, crypto.SHA256, digest, nil)
}

func RSAVerifySHA256(pk *rsa.PublicKey, digest []byte, sig []byte) error {
	return rsa.VerifyPSS(pk, crypto.SHA256, digest, sig, nil)
}

func RSAMaxPlaintextSize(pk *rsa.PublicKey) int {
	// The message must be no longer than the length of the public modulus minus
	// twice the hash length, minus a further 2.  (This is in bytes)
	//
	//  key length   max plaintext size
	//  1024        62
	//  2048        190
	//  3072        318
	//  4096        446

	h := sha256.New()
	return pk.Size() - (2 * h.Size()) - 2
}

func RSAEncrypt(pk *rsa.PublicKey, msg []byte) []byte {
	ciphertext, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pk, msg, nil)
	if err != nil {
		mu.Panicf("failed to RSA encrypt: %v", err)
	}
	return ciphertext
}

func RSADecrypt(sk *rsa.PrivateKey, ciphertext []byte) ([]byte, error) {
	return rsa.DecryptOAEP(sha256.New(), rand.Reader, sk, ciphertext, nil)
}
