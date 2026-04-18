package cryptofun

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

var RSAKeyLengths = [4]int{1024, 2048, 3072, 4096}

func BenchmarkGenerateRSAKeyPair(b *testing.B) {
	for _, keyLength := range RSAKeyLengths {
		b.Run(fmt.Sprintf("keyLength:%d", keyLength), func(b *testing.B) {
			for b.Loop() {
				_, _ = GenerateRSAKeyPair(keyLength)
			}
		})
	}
}

func BenchmarkRSASignSHA256(b *testing.B) {
	tmp := sha256.Sum256(OneKiBMessage)
	hash := tmp[:]
	for _, keyLength := range RSAKeyLengths {
		_, sk := GenerateRSAKeyPair(keyLength)
		b.Run(fmt.Sprintf("keyLength:%d", keyLength), func(b *testing.B) {
			for b.Loop() {
				_, _ = RSASignSHA256(sk, hash)
			}
		})
	}
}

func BenchmarkRSAVerifySHA256(b *testing.B) {
	tmp := sha256.Sum256(OneKiBMessage)
	hash := tmp[:]
	for _, keyLength := range RSAKeyLengths {
		pk, sk := GenerateRSAKeyPair(keyLength)
		sig, err := RSASignSHA256(sk, hash)
		if err != nil {
			b.Fatalf("RSASignSHA256 failed: %v", err)
		}
		b.Run(fmt.Sprintf("keyLength:%d", keyLength), func(b *testing.B) {
			for b.Loop() {
				_ = RSAVerifySHA256(pk, hash, sig)
			}
		})
	}
}

func BenchmarkRSAEncrypt(b *testing.B) {
	msg := GenerateAESKey()
	for _, keyLength := range RSAKeyLengths {
		pk, _ := GenerateRSAKeyPair(keyLength)
		b.Run(fmt.Sprintf("keyLength:%d", keyLength), func(b *testing.B) {
			for b.Loop() {
				_ = RSAEncrypt(pk, msg)
			}
		})
	}
}

func BenchmarkRSADecrypt(b *testing.B) {
	msg := GenerateAESKey()
	for _, keyLength := range RSAKeyLengths {
		pk, sk := GenerateRSAKeyPair(keyLength)
		ct := RSAEncrypt(pk, msg)
		b.Run(fmt.Sprintf("RSADecrypt-%d", keyLength), func(b *testing.B) {
			for b.Loop() {
				_, _ = RSADecrypt(sk, ct)
			}
		})
	}
}
