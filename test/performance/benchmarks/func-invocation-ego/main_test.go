package main

import (
	"encoding/json"
	"testing"

	"github.com/etclab/pre"
	"knative.dev/serving/pkg/samba"
)

// for the lack of a better place this test will create all the necessary keys
// required for function invocation benchmark tests
//
// --- client is the vegeta load generator (key pair, public key, public params)
// CLIENT_KP=”
// CLIENT_PK=”
// CLIENT_PP=”
// --- leader of the chain
// LEADER_KP=”
// LEADER_PK=”
// LEADER_PP=”
// --- member of the chain
// MEMBER_KP=”
// MEMBER_PK=”
// MEMBER_PP=”
func TestCreateKeys(t *testing.T) {
	// client is separate and will create its own public params
	clientPp := pre.NewPublicParams()
	clientKp := pre.KeyGen(clientPp)
	clientPk := clientKp.PK

	logPublicParams(t, clientPp, "Client")
	logKeyPair(t, clientKp, "Client")
	logPublicKey(t, clientPk, "Client")

	t.Logf("---\n")

	// leader and member will share the same public params
	leaderPp := pre.NewPublicParams()
	leaderKp := pre.KeyGen(leaderPp)
	leaderPk := leaderKp.PK

	logPublicParams(t, leaderPp, "Leader")
	logKeyPair(t, leaderKp, "Leader")
	logPublicKey(t, leaderPk, "Leader")

	memberPp := leaderPp // member shares the same public params as leader
	memberKp := pre.KeyGen(memberPp)
	memberPk := memberKp.PK

	logPublicParams(t, memberPp, "Member")
	logKeyPair(t, memberKp, "Member")
	logPublicKey(t, memberPk, "Member")
}

// serializes, json encodes and prints
func logPublicParams(t *testing.T, pp *pre.PublicParams, name string) {
	pks := new(samba.PublicParamsSerialized)
	pks.Serialize(pp)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		t.Errorf("error marshaling public params: %v", err)
	}
	t.Logf("-- %s PublicParams -- %s", name, string(jsonData))
}

// serializes, json encodes and prints
func logKeyPair(t *testing.T, kp *pre.KeyPair, name string) {
	kps := new(samba.KeyPairSerialized)
	kps.Serialize(kp)

	jsonData, err := json.Marshal(kps)
	if err != nil {
		t.Errorf("error marshaling key pair: %v", err)
	}
	t.Logf("-- %s KeyPair -- %s", name, string(jsonData))
}

// serializes, json encodes and prints
func logPublicKey(t *testing.T, pk *pre.PublicKey, name string) {
	pks := new(samba.PublicKeySerialized)
	pks.Serialize(pk)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		t.Errorf("error marshaling public key: %v", err)
	}
	t.Logf("-- %s PublicKey -- %s", name, string(jsonData))
}
