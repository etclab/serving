package cryptofun

import (
	"testing"

	"github.com/coinbase/kryptology/pkg/accumulator"
	"github.com/coinbase/kryptology/pkg/core/curves"
)

func BenchmarkMembershipWitness_New(b *testing.B) {
	curve := curves.BLS12381(&curves.PointBls12381G1{})
	var seed [32]byte

	sk, err := new(accumulator.SecretKey).New(curve, seed[:])
	if err != nil {
		b.Fatalf("cannot create secret key: %v", err)
	}
	_, err = sk.GetPublicKey(curve)
	if err != nil {
		b.Fatalf("cannot get public key: %v", err)
	}
	acc, err := new(accumulator.Accumulator).New(curve)
	if err != nil {
		b.Fatalf("cannot create accumulator: %v", err)
	}

	element := curve.Scalar.Hash([]byte("value1"))
	_, err = acc.Add(sk, element)
	if err != nil {
		b.Fatalf("cannot add an element to accumulator: %v", err)
	}

	for b.Loop() {
		_, err := new(accumulator.MembershipWitness).New(element, acc, sk)
		if err != nil {
			b.Fatalf("cannot create a witness: %v", err)
		}
	}
}

func BenchmarkMembershipWitness_Verify(b *testing.B) {
	curve := curves.BLS12381(&curves.PointBls12381G1{})
	var seed [32]byte

	sk, err := new(accumulator.SecretKey).New(curve, seed[:])
	if err != nil {
		b.Fatalf("cannot create secret key: %v", err)
	}
	pk, err := sk.GetPublicKey(curve)
	if err != nil {
		b.Fatalf("cannot get public key: %v", err)
	}
	acc, err := new(accumulator.Accumulator).New(curve)
	if err != nil {
		b.Fatalf("cannot create accumulator: %v", err)
	}

	element := curve.Scalar.Hash([]byte("value1"))
	_, err = acc.Add(sk, element)
	if err != nil {
		b.Fatalf("cannot add an element to accumulator: %v", err)
	}

	wit, err := new(accumulator.MembershipWitness).New(element, acc, sk)
	if err != nil {
		b.Fatalf("cannot create a witness: %v", err)
	}

	for b.Loop() {
		err := wit.Verify(pk, acc)
		if err != nil {
			b.Fatalf("membership witness failed verification: %v", err)
		}
	}
}

func BenchmarkMembershipProof(b *testing.B) {
	curve := curves.BLS12381(&curves.PointBls12381G1{})
	var seed [32]byte

	sk, err := new(accumulator.SecretKey).New(curve, seed[:])
	if err != nil {
		b.Fatalf("cannot create secret key: %v", err)
	}
	pk, err := sk.GetPublicKey(curve)
	if err != nil {
		b.Fatalf("cannot get public key: %v", err)
	}
	acc, err := new(accumulator.Accumulator).New(curve)
	if err != nil {
		b.Fatalf("cannot create accumulator: %v", err)
	}

	element := curve.Scalar.Hash([]byte("value1"))
	_, err = acc.Add(sk, element)
	if err != nil {
		b.Fatalf("cannot add an element to accumulator: %v", err)
	}

	wit, err := new(accumulator.MembershipWitness).New(element, acc, sk)
	if err != nil {
		b.Fatalf("cannot create a witness: %v", err)
	}

	for b.Loop() {
		params, err := new(accumulator.ProofParams).New(curve, pk, []byte("entropy"))
		if err != nil {
			b.Fatalf("cannot create a proof params: %v", err)
		}

		mpc, err := new(accumulator.MembershipProofCommitting).New(wit, acc, params, pk)
		if err != nil {
			b.Fatalf("cannot create a proof committing: %v", err)
		}

		challenge := curve.Scalar.Hash(mpc.GetChallengeBytes())
		proof := mpc.GenProof(challenge)
		_, err = proof.Finalize(acc, params, pk, challenge)
		if err != nil {
			b.Fatalf("cannot finalize proof: %v", err)
		}
	}
}
