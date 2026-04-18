package cryptofun

import (
	"crypto/sha512"
	"testing"
)

func BenchmarkGenerateEd25519KeyPair(b *testing.B) {
	for b.Loop() {
		_, _ = GenerateEd25519KeyPair()
	}
}

func BenchmarkEd25519phSign(b *testing.B) {
	_, sk := GenerateEd25519KeyPair()
	tmp := sha512.Sum512(OneKiBMessage)
	hash := tmp[:]
	for b.Loop() {
		_, err := Ed25519phSign(sk, hash)
		if err != nil {
			b.Fatalf("Ed25519phSign failed: %v", err)
		}
	}
}

func BenchmarkEd2519phVerify(b *testing.B) {
	pk, sk := GenerateEd25519KeyPair()
	tmp := sha512.Sum512(OneKiBMessage)
	hash := tmp[:]
	sig, err := Ed25519phSign(sk, hash)
	if err != nil {
		b.Fatalf("Ed25519phSign failed: %v", err)
	}
	for b.Loop() {
		valid := Ed25519phVerify(pk, hash, sig)
		if !valid {
			b.Fatalf("Ed25519phVerify failed: %v", err)
		}
	}
}
