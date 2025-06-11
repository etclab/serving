package pre

import (
	bls "github.com/cloudflare/circl/ecc/bls12381"
	"github.com/etclab/pre"
)

type PublicKeySerialized struct {
	G1toA []byte `json:"g1_to_a"`
	G2toA []byte `json:"g2_to_a"`
}

func (p *PublicKeySerialized) Serialize(pk *pre.PublicKey) {
	p.G1toA = pk.G1toA.Bytes()
	p.G2toA = pk.G2toA.Bytes()
}

func (p PublicKeySerialized) DeSerialize() (*pre.PublicKey, error) {
	g1 := &bls.G1{}
	g2 := &bls.G2{}

	err := g1.SetBytes(p.G1toA)
	if err != nil {
		return nil, err
	}

	err = g2.SetBytes(p.G2toA)
	if err != nil {
		return nil, err
	}

	return &pre.PublicKey{
		G1toA: g1,
		G2toA: g2,
	}, nil
}

type PublicParamsSerialized struct {
	G1 []byte `json:"g1"`
	G2 []byte `json:"g2"`
	Z  []byte `json:"z"`
}

func (p *PublicParamsSerialized) Serialize(pp *pre.PublicParams) error {
	z, err := pp.Z.MarshalBinary()
	if err != nil {
		return err
	}

	p.G1 = pp.G1.Bytes()
	p.G2 = pp.G2.Bytes()
	p.Z = z
	return err
}

func (p PublicParamsSerialized) DeSerialize() (*pre.PublicParams, error) {
	g1 := &bls.G1{}
	g2 := &bls.G2{}
	z := &bls.Gt{}

	err := g1.SetBytes(p.G1)
	if err != nil {
		return nil, err
	}

	err = g2.SetBytes(p.G2)
	if err != nil {
		return nil, err
	}

	err = z.UnmarshalBinary(p.Z)
	if err != nil {
		return nil, err
	}

	return &pre.PublicParams{
		G1: g1,
		G2: g2,
		Z:  z,
	}, nil
}

type Ciphertext1Serialized struct {
	Alpha []byte
	Beta  []byte
}

func (c *Ciphertext1Serialized) Serialize(ct1 *pre.Ciphertext1) error {
	alpha, err := ct1.Alpha.MarshalBinary()
	if err != nil {
		return err
	}
	c.Alpha = alpha
	c.Beta = ct1.Beta.Bytes()
	return nil
}

func (c Ciphertext1Serialized) DeSerialize() (*pre.Ciphertext1, error) {
	alpha := &bls.Gt{}
	beta := &bls.G1{}

	err := alpha.UnmarshalBinary(c.Alpha)
	if err != nil {
		return nil, err
	}

	err = beta.SetBytes(c.Beta)
	if err != nil {
		return nil, err
	}

	return &pre.Ciphertext1{
		Alpha: alpha,
		Beta:  beta,
	}, nil
}

type Ciphertext2Serialized struct {
	Alpha []byte
	Beta  []byte
}

func (c *Ciphertext2Serialized) Serialize(ct2 *pre.Ciphertext2) error {
	alpha, err := ct2.Alpha.MarshalBinary()
	if err != nil {
		return err
	}
	beta, err := ct2.Beta.MarshalBinary()
	if err != nil {
		return err
	}
	c.Alpha = alpha
	c.Beta = beta
	return nil
}

func (c Ciphertext2Serialized) DeSerialize() (*pre.Ciphertext2, error) {
	alpha := &bls.Gt{}
	beta := &bls.Gt{}

	err := alpha.UnmarshalBinary(c.Alpha)
	if err != nil {
		return nil, err
	}

	err = beta.UnmarshalBinary(c.Beta)
	if err != nil {
		return nil, err
	}

	return &pre.Ciphertext2{
		Alpha: alpha,
		Beta:  beta,
	}, nil
}

type ReEncryptionKeySerialized struct {
	RK []byte
}

func (r *ReEncryptionKeySerialized) Serialize(rk *pre.ReEncryptionKey) {
	r.RK = rk.RK.Bytes()
}

func (r ReEncryptionKeySerialized) DeSerialize() (*pre.ReEncryptionKey, error) {
	g2 := &bls.G2{}
	err := g2.SetBytes(r.RK)
	if err != nil {
		return nil, err
	}

	return &pre.ReEncryptionKey{
		RK: g2,
	}, nil
}
