package samba

import (
	"bytes"
	"encoding/json"
	"fmt"

	bls "github.com/cloudflare/circl/ecc/bls12381"
	"github.com/etclab/pre"
)

type KeyPairSerialized struct {
	PK PublicKeySerialized `json:"pk"`
	SK []byte              `json:"sk"`
}

func (kps *KeyPairSerialized) Serialize(kp *pre.KeyPair) error {
	kps.PK.Serialize(kp.PK)
	skBytes, err := kp.SK.A.MarshalBinary()
	if err != nil {
		return err
	}
	kps.SK = skBytes

	return nil
}

func (kps KeyPairSerialized) DeSerialize() (*pre.KeyPair, error) {
	publicKey, err := kps.PK.DeSerialize()
	if err != nil {
		return nil, err
	}

	A := new(bls.Scalar)
	err = A.UnmarshalBinary(kps.SK)
	if err != nil {
		return nil, err
	}

	secretKey := &pre.SecretKey{
		A: A,
	}

	return &pre.KeyPair{
		PK: publicKey,
		SK: secretKey,
	}, nil
}

func ParseKeyPair(kpBytes []byte) (*pre.KeyPair, error) {
	kps := new(KeyPairSerialized)
	err := json.NewDecoder(bytes.NewReader(kpBytes)).Decode(kps)
	if err != nil {
		return nil, fmt.Errorf("failed to decode key pair: %v", err)
	}

	keyPair, err := kps.DeSerialize()
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize key pair: %v", err)
	}

	return keyPair, nil
}

func ParsePublicKey(pkBytes []byte) (*pre.PublicKey, error) {
	pks := new(PublicKeySerialized)
	err := json.NewDecoder(bytes.NewReader(pkBytes)).Decode(pks)
	if err != nil {
		return nil, fmt.Errorf("failed to decode public key: %v", err)
	}

	publicKey, err := pks.DeSerialize()
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize public key: %v", err)
	}

	return publicKey, nil
}

func ParsePublicParams(ppBytes []byte) (*pre.PublicParams, error) {
	pps := new(PublicParamsSerialized)
	err := json.NewDecoder(bytes.NewReader(ppBytes)).Decode(pps)
	if err != nil {
		return nil, fmt.Errorf("failed to decode public params: %v", err)
	}

	publicParams, err := pps.DeSerialize()
	if err != nil {
		return nil, fmt.Errorf("failed to deserialize public params: %v", err)
	}

	return publicParams, nil
}

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

type SambaMessage struct {
	Target        string                `json:"target"`
	IsReEncrypted bool                  `json:"is_re_encrypted"`
	WrappedKey1   Ciphertext1Serialized `json:"wrapped_key1"` // Encrypted bls.Gt that derives to AES key
	WrappedKey2   Ciphertext2Serialized `json:"wrapped_key2"` // Re-encrypted bls.Gt that derives to AES key
	Ciphertext    []byte                `json:"ciphertext"`   // plaintext (just a string for now) encrypted under the AES key
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
