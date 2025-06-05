package pre

import (
	bls "github.com/cloudflare/circl/ecc/bls12381"
)

type PublicParams struct {
	G1 *bls.G1
	G2 *bls.G2
	Z  *bls.Gt
}

func NewPublicParams() *PublicParams {
	pp := new(PublicParams)

	pp.G1 = bls.G1Generator()
	pp.G2 = bls.G2Generator()
	pp.Z = bls.Pair(pp.G1, pp.G2)

	return pp
}

type PublicKey struct {
	G1toA *bls.G1
	G2toA *bls.G2
}

type SecretKey struct {
	A *bls.Scalar
}

type ReEncryptionKey struct {
	RK *bls.G2
}

type KeyPair struct {
	PK *PublicKey
	SK *SecretKey
}

func KeyGen(pp *PublicParams) *KeyPair {
	sk := new(SecretKey)
	sk.A = randomScalar()

	pk := new(PublicKey)
	pk.G1toA = new(bls.G1)
	pk.G1toA.ScalarMult(sk.A, pp.G1)
	pk.G2toA = new(bls.G2)
	pk.G2toA.ScalarMult(sk.A, pp.G2)

	return &KeyPair{
		PK: pk,
		SK: sk,
	}
}

func ReEncryptionKeyGen(_ *PublicParams, aliceSK *SecretKey, bobPK *PublicKey) *ReEncryptionKey {
	aInv := new(bls.Scalar)
	aInv.Inv(aliceSK.A)

	rk := new(bls.G2)
	rk.ScalarMult(aInv, bobPK.G2toA)

	return &ReEncryptionKey{
		RK: rk,
	}
}

type Ciphertext1 struct {
	Alpha *bls.Gt
	Beta  *bls.G1
}

func Encrypt(pp *PublicParams, m *bls.Gt, pk *PublicKey) *Ciphertext1 {
	r := randomScalar()
	alpha := new(bls.Gt)
	alpha.Exp(pp.Z, r)
	alpha.Mul(alpha, m)

	beta := new(bls.G1)
	beta.ScalarMult(r, pk.G1toA)

	return &Ciphertext1{
		Alpha: alpha,
		Beta:  beta,
	}
}

type Ciphertext2 struct {
	Alpha *bls.Gt
	Beta  *bls.Gt
}

func ReEncrypt(pp *PublicParams, rk *ReEncryptionKey, ct1 *Ciphertext1) *Ciphertext2 {
	beta := bls.Pair(ct1.Beta, rk.RK)

	return &Ciphertext2{
		Alpha: ct1.Alpha,
		Beta:  beta,
	}
}

func Decrypt1(pp *PublicParams, ct1 *Ciphertext1, sk *SecretKey) *bls.Gt {
	aInv := new(bls.Scalar)
	aInv.Inv(sk.A)
	tmp1 := new(bls.G2)
	tmp1.ScalarMult(aInv, pp.G2)

	tmp2 := bls.Pair(ct1.Beta, tmp1)
	tmp2.Inv(tmp2)

	m := new(bls.Gt)
	m.Mul(ct1.Alpha, tmp2)

	return m
}

func Decrypt2(pp *PublicParams, ct2 *Ciphertext2, sk *SecretKey) *bls.Gt {
	bInv := new(bls.Scalar)
	bInv.Inv(sk.A)
	tmp := new(bls.Gt)
	tmp.Exp(ct2.Beta, bInv)
	tmp.Inv(tmp)

	m := new(bls.Gt)
	m.Mul(ct2.Alpha, tmp)

	return m
}
