package cryptofun

import (
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"testing"

	"github.com/etclab/mu"
)

func BenchmarkGenerateECDSAKeyPair(b *testing.B) {
	curves := []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()}

	for _, curve := range curves {
		b.Run(fmt.Sprintf("curve:%s", curve.Params().Name), func(b *testing.B) {
			for b.Loop() {
				_, _ = GenerateECDSAKeyPair(curve)
			}
		})
	}
}

func BenchmarkECDSASignASN1(b *testing.B) {
	curves := []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()}

	for _, curve := range curves {
		b.Run(fmt.Sprintf("curve:%s", curve.Params().Name), func(b *testing.B) {
			_, sk := GenerateECDSAKeyPair(curve)
			var hash []byte
			switch curve {
			case elliptic.P256():
				tmp := sha256.Sum256(OneKiBMessage)
				hash = tmp[:]
			case elliptic.P384():
				tmp := sha512.Sum384(OneKiBMessage)
				hash = tmp[:]
			case elliptic.P521():
				tmp := sha512.Sum512(OneKiBMessage)
				hash = tmp[:]
			default:
				mu.BUG("unknown curve")
			}
			for b.Loop() {
				_, err := ECDSASignASN1(sk, hash)
				if err != nil {
					b.Fatalf("ECDSASignASN1 failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkECDSAVerifyASN1(b *testing.B) {
	curves := []elliptic.Curve{elliptic.P256(), elliptic.P384(), elliptic.P521()}

	for _, curve := range curves {
		b.Run(fmt.Sprintf("curve:%s", curve.Params().Name), func(b *testing.B) {
			pk, sk := GenerateECDSAKeyPair(curve)
			var hash []byte
			switch curve {
			case elliptic.P256():
				tmp := sha256.Sum256(OneKiBMessage)
				hash = tmp[:]
			case elliptic.P384():
				tmp := sha512.Sum384(OneKiBMessage)
				hash = tmp[:]
			case elliptic.P521():
				tmp := sha512.Sum512(OneKiBMessage)
				hash = tmp[:]
			default:
				mu.BUG("unknown curve")
			}
			sig, err := ECDSASignASN1(sk, hash)
			if err != nil {
				b.Fatalf("ECDSASignASN1 failed: %v", err)
			}
			for b.Loop() {
				valid := ECDSAVerifyASN1(pk, hash, sig)
				if !valid {
					b.Fatal("ECDSAVerifyASN1 failed")
				}
			}
		})
	}

}
