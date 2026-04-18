package cryptofun

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"

	"github.com/etclab/mu"
)

func GenerateEd25519KeyPair() (ed25519.PublicKey, ed25519.PrivateKey) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		mu.Panicf("ed25519.GenerateKey: %v", err)
	}
	return pk, sk
}

// hash must be sha512
func Ed25519phSign(sk ed25519.PrivateKey, hash []byte) ([]byte, error) {
	return sk.Sign(rand.Reader, hash, crypto.SHA512)
}

// hash must be sha512
func Ed25519phVerify(pk ed25519.PublicKey, hash, sig []byte) bool {
	opts := ed25519.Options{
		Hash: crypto.SHA512,
	}
	err := ed25519.VerifyWithOptions(pk, hash, sig, &opts)
	return err == nil
}
