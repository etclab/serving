package bgls

import (
	"encoding/json"
	"fmt"

	bls "github.com/cloudflare/circl/ecc/bls12381"
	bgls03 "github.com/etclab/ncircl/aggsig/bgls03"
)

// type PublicKey struct {
// 	V *bls.G2
// }

type PublicKeySerialized struct {
	V []byte `json:"v"`
}

func (pks *PublicKeySerialized) Serialize(pk *bgls03.PublicKey) {
	pks.V = pk.V.BytesCompressed()
}

func (pks PublicKeySerialized) DeSerialize() (*bgls03.PublicKey, error) {
	v := &bls.G2{}

	err := v.SetBytes(pks.V)
	if err != nil {
		return nil, err
	}

	return &bgls03.PublicKey{
		V: v,
	}, nil
}

// PrivateKeySerialized provides serialization for bgls03.PrivateKey.
// The private key contains a bls.Scalar which can be marshaled to bytes.
type PrivateKeySerialized struct {
	X []byte `json:"x"`
}

func (sks *PrivateKeySerialized) Serialize(sk *bgls03.PrivateKey) error {
	if sk == nil || sk.X == nil {
		return fmt.Errorf("cannot serialize nil private key")
	}
	xBytes, err := sk.X.MarshalBinary()
	if err != nil {
		return fmt.Errorf("failed to marshal scalar: %w", err)
	}
	sks.X = xBytes
	return nil
}

func (sks PrivateKeySerialized) DeSerialize() (*bgls03.PrivateKey, error) {
	if len(sks.X) == 0 {
		return nil, fmt.Errorf("cannot deserialize empty private key bytes")
	}
	x := new(bls.Scalar)
	err := x.UnmarshalBinary(sks.X)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal scalar: %w", err)
	}
	return &bgls03.PrivateKey{
		X: x,
	}, nil
}

// type PublicParams struct {
// 	G1 *bls.G1
// 	G2 *bls.G2
// }

type PublicParamsSerialized struct {
	G1 []byte `json:"g1"`
	G2 []byte `json:"g2"`
}

func (pps *PublicParamsSerialized) Serialize(pp *bgls03.PublicParams) {
	pps.G1 = pp.G1.BytesCompressed()
	pps.G2 = pp.G2.BytesCompressed()
}

func (pps PublicParamsSerialized) DeSerialize() (*bgls03.PublicParams, error) {
	g1 := &bls.G1{}
	g2 := &bls.G2{}

	err := g1.SetBytes(pps.G1)
	if err != nil {
		return nil, err
	}

	err = g2.SetBytes(pps.G2)
	if err != nil {
		return nil, err
	}

	return &bgls03.PublicParams{
		G1: g1,
		G2: g2,
	}, nil
}

// public keying material
type PkmMessage struct {
	Pp PublicParamsSerialized `json:"pp"`
	Pk PublicKeySerialized    `json:"pk"`
	Id string                 `json:"id"`
}

func HashPkm(pp *bgls03.PublicParams, pk *bgls03.PublicKey, podId string) ([]byte, error) {
	pkms := PkmMessage{}
	pkms.Pp.Serialize(pp)
	pkms.Pk.Serialize(pk)
	pkms.Id = podId

	jsonBytes, err := json.Marshal(pkms)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal failed: %v", err)
	}

	return jsonBytes, nil
}
