package pre

import (
	"crypto/rand"
	"crypto/sha256"
	"io"

	bls "github.com/cloudflare/circl/ecc/bls12381"
	"github.com/etclab/mu"
	"golang.org/x/crypto/hkdf"
)

const (
	DST_G2        = "QUUX-V01-CS02-with-BLS12381G2_XMD:SHA-256_SSWU_RO_"
	DST_G1        = "QUUX-V01-CS02-with-BLS12381G1_XMD:SHA-256_SSWU_RO_"
	Aes256KeySize = 32
)

// hash arbitrary message ([]byte) to bls.Gt
// based on: https://github.com/cloudflare/circl/blob/main/ecc/bls12381/hash_test.go
func HashMsgGt(msg []byte) *bls.Gt {
	g1 := new(bls.G1)
	g1.Hash(msg, []byte(DST_G1))
	g2 := new(bls.G2)
	g2.Hash(msg, []byte(DST_G2))
	return bls.Pair(g1, g2)
}

func KdfGtToAes256(gt *bls.Gt) []byte {
	bytes, err := gt.MarshalBinary()
	if err != nil {
		mu.Panicf("Gt.MarshaBinary failed: %v", err)
	}

	kdf := hkdf.New(sha256.New, bytes, nil, nil)

	aesKey := make([]byte, Aes256KeySize)
	_, err = io.ReadFull(kdf, aesKey)
	if err != nil {
		mu.Panicf("io.ReadFull failed: %v", err)
	}

	return aesKey
}

func randomScalar() *bls.Scalar {
	z := new(bls.Scalar)
	z.Random(rand.Reader)
	return z
}

func RandomGt() *bls.Gt {
	a := randomScalar()
	b := randomScalar()

	g1 := bls.G1Generator()
	g2 := bls.G2Generator()

	g1.ScalarMult(a, g1)
	g2.ScalarMult(b, g2)

	z := bls.Pair(g1, g2)
	return z
}
