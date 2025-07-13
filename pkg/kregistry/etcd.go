package kregistry

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/etclab/ncircl/aggsig/bgls03"
	"github.com/etclab/pre"
	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/bgls"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"

	"github.com/edgelesssys/ego/enclave"
)

//	Env:{
//		ServingNamespace:default
//		ServingService:second
//		ServingConfiguration:second
//		ServingRevision:second-00001
//		ServingPod:second-00001-deployment-55d95d8764-bd4tq
//	 	ServingPodIP:10.244.0.98
//	}
type KeyRegistry struct {
	StartedLeading atomic.Bool

	client *clientv3.Client
	// was pod able to connect to etcd?
	IsEtcdReady chan struct{}

	// is pod read for proxy re-encryption?
	// when does a pod become ready for proxy re-encryption?
	// if it's a leader, it's ready when it has public params and keypair
	// if it's a member, it's ready when it has re-encryption key from leader
	IsPodPreReady chan struct{}

	// InstanceId is the ip address of the pod
	InstanceId string
	// RevisionId is the unique id for the function
	// and identifies a deployed revision of the function
	FunctionId string
	// sometimes we need same function id for two different revisions
	// if function id is same the keys stored at etcd will be accessible to multiple revisions
	// also the lease lock name will be the same and multiple revisions will share the lease
	OverriddenFunctionId  string // for testing
	OverriddenServiceName string // for testing
	ServiceName           string
	// PodId is the unique id for the pod
	PodId        string
	KeyPair      *pre.KeyPair
	PublicParams *pre.PublicParams
	// Crypto       SambaCrypto

	// start: state relevant for both leader and member
	EveryLeaderPublicKey   map[string]*pre.PublicKey
	muEveryLeaderPublicKey sync.RWMutex
	//
	EveryLeaderPublicParams   map[string]*pre.PublicParams
	muEveryLeaderPublicParams sync.RWMutex
	// end: state relevant for both leader and member

	// start: state when queue-proxy is a leader
	LeaPublicParams []*pre.PublicParams
	LeaKeyPair      []*pre.KeyPair
	muLeaKeys       sync.RWMutex
	// LeaMem* means the data for leader is received from the members
	LeaMemPublicKeys map[string]*pre.PublicKey // key is member pod id
	// leader generates re-encryption key using member public keys
	LeaMemReEncryptionKeys map[string]*pre.ReEncryptionKey
	// end: state when queue-proxy is a leader

	// start: state when queue-proxy is a member
	MemKeyPair   map[string]*pre.KeyPair // key is leader pod id
	muMemKeyPair sync.RWMutex
	//
	// MemLeader* means the data for member is received from the leader
	MemLeaderIds   []string
	muMemLeaderIds sync.RWMutex
	//
	MemLeaderPublicKey   map[string]*pre.PublicKey // key is leader pod id
	muMemLeaderPublicKey sync.RWMutex
	//
	MemLeaderPublicParams   map[string]*pre.PublicParams
	muMemLeaderPublicParams sync.RWMutex
	//
	MemLeaderReEncryptionKey   map[string]*pre.ReEncryptionKey
	muMemLeaderReEncryptionKey sync.RWMutex
	// end: state when queue-proxy is a member

	// function chains state
	functionChains []string
	// key is function chain id, value is the series of services in the chain
	functionChainServices map[string]string

	// -- mostly relevant for testing --
	// RSA private key received from environment variable RSA_SK (for now)
	// will be received from a trusted key registry via attested tls
	RSASecretKey *rsa.PrivateKey

	ClientPp *pre.PublicParams
	ClientPk *pre.PublicKey

	// relevant for bgls03 signature scheme
	SigPp             *bgls03.PublicParams
	SigPk             *bgls03.PublicKey
	SigSk             *bgls03.PrivateKey
	AttestationReport []byte

	// key is nonce, value is the aggregate signature over the nonce|function-id
	AggregateSignature map[string]string
	// key is nonce, value is the functions that have signed the nonce
	RunningFunctionChain map[string]string
	muSignatureLock      sync.RWMutex
}

func (kr *KeyRegistry) StoreAggSignatureAndChain(nonce, functionChain, aggSignature string) {
	kr.muSignatureLock.Lock()
	defer kr.muSignatureLock.Unlock()

	if kr.AggregateSignature == nil {
		kr.AggregateSignature = make(map[string]string)
	}
	if kr.RunningFunctionChain == nil {
		kr.RunningFunctionChain = make(map[string]string)
	}
	kr.AggregateSignature[nonce] = aggSignature
	kr.RunningFunctionChain[nonce] = functionChain
}

func (kr *KeyRegistry) GetAggSignatureAndChain(nonce string) (string, string) {
	kr.muSignatureLock.RLock()
	defer kr.muSignatureLock.RUnlock()

	aggSig := ""
	chain := ""
	if kr.AggregateSignature != nil {
		aggSig = kr.AggregateSignature[nonce]
	}
	if kr.RunningFunctionChain != nil {
		chain = kr.RunningFunctionChain[nonce]
	}
	return chain, aggSig
}

func (kr *KeyRegistry) SetupSignature(setupSignature bool, podId string) {
	logDev := mutil.LogWithPrefix("dev - SetupSignature")
	if setupSignature {
		logDev("AttachSignature is enabled, generating bgls03 pp and key pair")
		pp := bgls03.NewPublicParams()
		pk, sk := bgls03.KeyGen(pp)

		kr.SigPp = pp
		kr.SigPk = pk
		kr.SigSk = sk

		// hash pp,pk and embed inside enclave report
		pkmHash, err := bgls.HashPkm(pp, pk, podId)
		if err != nil {
			logDev("HashPkm failed: %v", err)
		}
		hash := sha256.Sum256(pkmHash)

		report, err := enclave.GetRemoteReport(hash[:])
		if err != nil {
			logDev("failed to get attestation report from ego: %v", err)
		}

		bglsPrefix := "bgls03/" + podId
		// send pp,pk to etcd
		// send report to etcd
		pkKey := bglsPrefix + "/pk"
		err = kr.StoreBglsPublicKey(pkKey, pk)
		if err != nil {
			logDev("Error storing bgls public key in KeyRegistry: %v", err)
		}

		ppKey := bglsPrefix + "/pp"
		err = kr.StoreBglsPublicParams(ppKey, pp)
		if err != nil {
			logDev("Error storing bgls public params in KeyRegistry: %v", err)
		}

		reportKey := bglsPrefix + "/report"
		err = kr.StoreAttestationReport(reportKey, report)
		if err != nil {
			logDev("Error storing attestation report in KeyRegistry: %v", err)
		}
		kr.AttestationReport = report
	} else {
		logDev("AttachSignature is disabled, skipping bgls03 pp and key pair generation")
	}
}

func (kr *KeyRegistry) StoreBglsPublicKey(key string, publicKey *bgls03.PublicKey) error {
	pks := new(bgls.PublicKeySerialized)
	pks.Serialize(publicKey)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		return fmt.Errorf("error marshaling public key message: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) StoreBglsPublicParams(key string, publicParams *bgls03.PublicParams) error {
	pks := new(bgls.PublicParamsSerialized)
	pks.Serialize(publicParams)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		return fmt.Errorf("error marshaling public params message: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) StoreAttestationReport(key string, report []byte) error {
	jsonData, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("error marshaling attestation report: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) GetFunctionId(servingRevisionName string) string {
	if kr.OverriddenFunctionId != "" {
		return kr.OverriddenFunctionId
	} else {
		return servingRevisionName
	}
}

func (kr *KeyRegistry) GetServiceName(serviceName string) string {
	if kr.OverriddenServiceName != "" {
		return kr.OverriddenServiceName
	} else {
		return serviceName
	}
}

// load the client_pp,client_pk early instead of parsing it from env vars
// on every request/response
func (kr *KeyRegistry) LoadMyEnvVars(serviceName string) {
	logDev := mutil.LogWithPrefix("dev - LoadMyEnvVars")

	service_names := os.Getenv("SERVICE_NAMES")
	function_ids := os.Getenv("FUNCTION_IDS")

	if service_names != "" && function_ids != "" {
		logDev("Overriding function id and service name with %s and %s", function_ids, service_names)

		// serviceA:alt_nameA,serviceB:alt_nameA
		serviceNameMap := make(map[string]string)
		names := strings.Split(service_names, ",")
		for _, name := range names {
			keyPair := strings.Split(name, ":")
			serviceNameMap[keyPair[0]] = keyPair[1]
		}

		value, exists := serviceNameMap[serviceName]
		if exists {
			logDev("Overriding service name for %s to %s", serviceName, value)
			kr.OverriddenServiceName = value
		} else {
			logDev("No override for service name %s", serviceName)
		}

		functionIdMap := make(map[string]string)
		ids := strings.Split(function_ids, ",")
		for _, id := range ids {
			keyPair := strings.Split(id, ":")
			functionIdMap[keyPair[0]] = keyPair[1]
		}

		value, exists = functionIdMap[serviceName]
		if exists {
			logDev("Overriding function id for %s to %s", serviceName, value)
			kr.OverriddenFunctionId = value
		} else {
			logDev("No override for function id for service %s", serviceName)
		}
	} else {
		logDev("No overrides for function id and service name")
	}

	var err error
	var rsaSecretKey *rsa.PrivateKey
	// try reading with RSA private key if RSA_SK is set
	rsaSkStr := os.Getenv("RSA_SK")

	if rsaSkStr != "" {
		rsaSecretKey, err = mutil.UnmarshalRSAPrivateKeyFromPEM([]byte(rsaSkStr))
		if err != nil {
			log.Fatalf("failed to parse RSA private key: %v", err.Error())
		}
		logDev("RSA_SK is set, using it for decryption")

		kr.RSASecretKey = rsaSecretKey
	} else {
		logDev("RSA_SK is not set")
	}

	// load the client_pp,client_pk as well if in env vars
	pps := os.Getenv("CLIENT_PP")
	pks := os.Getenv("CLIENT_PK")

	if pps != "" && pks != "" {
		pp, err := samba.ParsePublicParams([]byte(pps))
		if err != nil {
			logDev("failed to parse public params: %v", err.Error())
		} else {
			logDev("Parsed CLIENT_PP successfully.")
		}
		pk, err := samba.ParsePublicKey([]byte(pks))
		if err != nil {
			logDev("failed to parse public key: %v", err.Error())
		} else {
			logDev("Parsed CLIENT_PK successfully.")
		}

		kr.ClientPp = pp
		kr.ClientPk = pk
	} else {
		logDev("CLIENT_PP or CLIENT_PK is not set in environment variables.")
	}
}

func (kr *KeyRegistry) SafeWriteEveryLeaderPublicParams(leaderServiceName string, publicParams *pre.PublicParams) *pre.PublicParams {
	return mutil.GSafeWriteToMap(leaderServiceName, publicParams, &kr.EveryLeaderPublicParams, &kr.muEveryLeaderPublicParams)
}

func (kr *KeyRegistry) SafeReadEveryLeaderPublicParams(leaderServiceName string) *pre.PublicParams {
	return mutil.GSafeReadFromMap(leaderServiceName, kr.EveryLeaderPublicParams, &kr.muEveryLeaderPublicParams)
}

func (kr *KeyRegistry) SafeWriteEveryLeaderPublicKey(leaderServiceName string, publicKey *pre.PublicKey) *pre.PublicKey {
	return mutil.GSafeWriteToMap(leaderServiceName, publicKey, &kr.EveryLeaderPublicKey, &kr.muEveryLeaderPublicKey)
}

func (kr *KeyRegistry) SafeReadEveryLeaderPublicKey(leaderServiceName string) *pre.PublicKey {
	return mutil.GSafeReadFromMap(leaderServiceName, kr.EveryLeaderPublicKey, &kr.muEveryLeaderPublicKey)
}

func (kr *KeyRegistry) SafeWriteMemKeyPair(leaderPodId string, keyPair *pre.KeyPair) *pre.KeyPair {
	return mutil.GSafeWriteToMap(leaderPodId, keyPair, &kr.MemKeyPair, &kr.muMemKeyPair)
}

func (kr *KeyRegistry) SafeReadMemKeyPair(leaderId string) *pre.KeyPair {
	return mutil.GSafeReadFromMap(leaderId, kr.MemKeyPair, &kr.muMemKeyPair)
}

func (kr *KeyRegistry) SafeWriteMemLeaderReEncryptionKey(leaderPodId string, reEncryptionKey *pre.ReEncryptionKey) *pre.ReEncryptionKey {
	return mutil.GSafeWriteToMap(leaderPodId, reEncryptionKey, &kr.MemLeaderReEncryptionKey, &kr.muMemLeaderReEncryptionKey)
}

func (kr *KeyRegistry) SafeReadMemLeaderReEncryptionKey(leaderId string) *pre.ReEncryptionKey {
	return mutil.GSafeReadFromMap(leaderId, kr.MemLeaderReEncryptionKey, &kr.muMemLeaderReEncryptionKey)
}

func (kr *KeyRegistry) SafeWriteMemLeaderPublicParams(leaderPodId string, publicParams *pre.PublicParams) *pre.PublicParams {
	return mutil.GSafeWriteToMap(leaderPodId, publicParams, &kr.MemLeaderPublicParams, &kr.muMemLeaderPublicParams)
}

func (kr *KeyRegistry) SafeReadMemLeaderPublicParams(leaderId string) *pre.PublicParams {
	return mutil.GSafeReadFromMap(leaderId, kr.MemLeaderPublicParams, &kr.muMemLeaderPublicParams)
}

func (kr *KeyRegistry) SafeWriteMemLeaderPublicKey(leaderPodId string, publicKey *pre.PublicKey) *pre.PublicKey {
	return mutil.GSafeWriteToMap(leaderPodId, publicKey, &kr.MemLeaderPublicKey, &kr.muMemLeaderPublicKey)
}

func (kr *KeyRegistry) SafeReadMemLeaderPublicKey(leaderId string) *pre.PublicKey {
	return mutil.GSafeReadFromMap(leaderId, kr.MemLeaderPublicKey, &kr.muMemLeaderPublicKey)
}

func (kr *KeyRegistry) SafeWriteMemLeaderId(leaderId string) {
	kr.muMemLeaderIds.Lock()
	defer kr.muMemLeaderIds.Unlock()
	kr.MemLeaderIds = append(kr.MemLeaderIds, leaderId)
}

func (kr *KeyRegistry) SafeReadMemLeaderId() string {
	kr.muMemLeaderIds.RLock()
	defer kr.muMemLeaderIds.RUnlock()
	ids := kr.MemLeaderIds
	return ids[len(ids)-1]
}

func (kr *KeyRegistry) SafeReadLeaderKeys() (*pre.PublicParams, *pre.KeyPair) {
	kr.muLeaKeys.RLock()
	defer kr.muLeaKeys.RUnlock()
	pp := kr.LeaPublicParams
	kp := kr.LeaKeyPair
	return pp[len(pp)-1], kp[len(kp)-1]
}

func (kr *KeyRegistry) SafeWriteLeaderKeys(keyPair *pre.KeyPair, publicParams *pre.PublicParams) {
	kr.muLeaKeys.Lock()
	defer kr.muLeaKeys.Unlock()
	kr.LeaPublicParams = append(kr.LeaPublicParams, publicParams)
	kr.LeaKeyPair = append(kr.LeaKeyPair, keyPair)
}

func (kr *KeyRegistry) MarkPodPreReady() {
	select {
	case <-kr.IsPodPreReady:
		// already closed
	default:
		close(kr.IsPodPreReady)
	}
}

func (kr *KeyRegistry) Client() *clientv3.Client {
	// this channel is closed as soon as the etcd client is ready
	// and therefore will unblock immediately for any subsequent calls
	<-kr.IsEtcdReady
	return kr.client
}

func (kr *KeyRegistry) StoreKV(key string, value string) error {
	logDev := mutil.LogWithPrefix("dev - StoreKV")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := kr.Client().Put(ctx, key, value)
	if err != nil {
		return fmt.Errorf("failed to store key: %s in etcd: %v", key, err)
	}

	logDev("stored key %v in etcd", key)
	return nil
}

func (kr *KeyRegistry) StorePublicKey(key string, publicKey *pre.PublicKey) error {
	pks := new(samba.PublicKeySerialized)
	pks.Serialize(publicKey)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		return fmt.Errorf("error marshaling public key message: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) StoreReEncryptionKey(key string, reEncryptionKey *pre.ReEncryptionKey) error {
	rks := new(samba.ReEncryptionKeySerialized)
	rks.Serialize(reEncryptionKey)

	jsonData, err := json.Marshal(rks)
	if err != nil {
		return fmt.Errorf("error marshaling public key message: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) StorePublicParams(key string, publicParams *pre.PublicParams) error {
	pks := new(samba.PublicParamsSerialized)
	pks.Serialize(publicParams)

	jsonData, err := json.Marshal(pks)
	if err != nil {
		return fmt.Errorf("error marshaling public params message: %v", err)
	}

	return kr.StoreKV(key, string(jsonData))
}

func (kr *KeyRegistry) InitEtcdWithRetry() {
	logDev := mutil.LogWithPrefix("dev - InitEtcdWithRetry")

	backoff := 5 * time.Second
	maxBackoff := 2 * time.Minute
	maxAttempts := 20
	attempts := 0

	for {
		attempts++
		client, err := tryConnectToEtcd()
		if err == nil {
			logDev("Successfully connected to etcd after %d attempts", attempts)
			kr.client = client
			close(kr.IsEtcdReady) // Signal that etcd is ready
			return
		}

		logDev("Failed to connect to etcd (attempt %d): %v", attempts, err)

		if maxAttempts > 0 && attempts >= maxAttempts {
			logDev("Max connection attempts reached. Giving up on etcd connection")
			kr.client = nil
		}

		// Sleep with exponential backoff, capped at maxBackoff
		time.Sleep(backoff)
		backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
	}
}

func tryConnectToEtcd() (*clientv3.Client, error) {
	logDev := mutil.LogWithPrefix("dev - tryConnectToEtcd")

	etcdClient, err := clientv3.New(clientv3.Config{
		Endpoints:   []string{"etcd.knative-serving.svc:2379"},
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create etcd client: %w", err)
	}

	// Test connection with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	_, err = etcdClient.Status(ctx, etcdClient.Endpoints()[0])
	if err != nil {
		etcdClient.Close()
		return nil, fmt.Errorf("etcd server unreachable: %w", err)
	}

	logDev("Connected to etcd: %v", etcdClient.Endpoints())
	return etcdClient, nil
}

// TODO: how do I cancel etcd watch? after leader re-election

// member watches for re-encryption key from leaders under file
// "members/<leader-pod-id>/reEncryptionKeys/<member-pod-id>"
func (kr *KeyRegistry) ListWatchReEncryptionKey(reEncKeyDir, leaderPodId string) {
	logDev := mutil.LogWithPrefix("dev - ListWatchReEncryptionKey")

	currentRevision := kr.FetchExistingReEncryptionKeys(reEncKeyDir, leaderPodId)
	if currentRevision < 0 {
		logDev("Failed to fetch existing re-encryption keys, cannot start watch")
		return
	}

	rch := kr.Client().Watch(context.Background(), reEncKeyDir, clientv3.WithRev(currentRevision+1))
	logDev("Watching etcd key: %s, rev: %d", reEncKeyDir, currentRevision+1)
	for wresp := range rch {
		if wresp.Canceled {
			logDev("etcd watch canceled: %v", wresp.Err())
			return
		}

		logDev("etcd watch response: %+v", wresp)
		for _, ev := range wresp.Events {
			logDev("type: %s, key: %q\n", ev.Type, ev.Kv.Key)

			if ev.Type == clientv3.EventTypePut {
				key := string(ev.Kv.Key)
				value := ev.Kv.Value

				logDev("Received PUT event for key: %s, value: %s", key, value)
				keyStr := string(key)

				err := kr.HandleMemberReEncryptionKey(keyStr, leaderPodId, value)
				if err != nil {
					logDev("Failed to save member re-encryption key: %v", err)
					continue
				}
			}
		}
	}
}

// return the current revision
func (kr *KeyRegistry) FetchExistingReEncryptionKeys(reEncKeyDir, leaderPodId string) int64 {
	logDev := mutil.LogWithPrefix("dev - FetchExistingReEncryptionKeys")

	reEncKeyRes, err := kr.Client().Get(context.Background(), reEncKeyDir)
	if err != nil {
		logDev("failed to fetch existing re-encryption keys from etcd: %v", err)
		return -1
	}

	logDev("fetched %d existing re-encryption keys from etcd", len(reEncKeyRes.Kvs))
	for _, kv := range reEncKeyRes.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)
		keyStr := string(key)

		err := kr.HandleMemberReEncryptionKey(keyStr, leaderPodId, value)
		if err != nil {
			logDev("Failed to save member re-encryption keys: %v", err)
			continue
		}
	}

	logDev("current revision is %d", reEncKeyRes.Header.Revision)
	return reEncKeyRes.Header.Revision
}

// fetch existing member public keys from etcd and return the current revision
func (kr *KeyRegistry) FetchExistingMemberPublicKeys(memberPublicKeyDir, leaderPodId string) int64 {
	logDev := mutil.LogWithPrefix("dev - FetchExistingMemberPublicKeys")

	pubKeys, err := kr.Client().Get(context.Background(), memberPublicKeyDir, clientv3.WithPrefix())
	if err != nil {
		logDev("failed to fetch existing member public keys from etcd: %v", err)
		return -1
	}

	logDev("fetched %d public keys from etcd", len(pubKeys.Kvs))
	for _, kv := range pubKeys.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)
		keyStr := string(key)

		err := kr.HandleMemberPublicKey(keyStr, leaderPodId, value)
		if err != nil {
			logDev("Failed to save member public key: %v", err)
			continue
		}
	}

	logDev("current revision is %d", pubKeys.Header.Revision)
	return pubKeys.Header.Revision
}

// leader watches for public keys from members under directory
// "members/<leader-pod-id>/publicKey/*"
func (kr *KeyRegistry) ListWatchMemberPublicKeys(memberPublicKeyDir, leaderPodId string) {
	logDev := mutil.LogWithPrefix("dev - ListWatchMemberPublicKeys")

	currentRevision := kr.FetchExistingMemberPublicKeys(memberPublicKeyDir, leaderPodId)
	if currentRevision < 0 {
		logDev("Failed to fetch existing member public keys, cannot start watch")
		return
	}

	// start watching for new member public keys from the next revision
	rch := kr.Client().Watch(context.Background(), memberPublicKeyDir, clientv3.WithPrefix(), clientv3.WithRev(currentRevision+1))
	logDev("Watching etcd keys with prefix: %s, rev: %d", memberPublicKeyDir, currentRevision+1)
	for wresp := range rch {
		if wresp.Canceled {
			logDev("etcd watch canceled: %v", wresp.Err())
			return
		}

		logDev("etcd watch response: %+v", wresp)
		for _, ev := range wresp.Events {
			logDev("type: %s, key: %q\n", ev.Type, ev.Kv.Key)

			if ev.Type == clientv3.EventTypePut {
				key := string(ev.Kv.Key)
				value := ev.Kv.Value

				logDev("Received PUT event for key: %s, value: %s", key, value)
				keyStr := string(key)

				err := kr.HandleMemberPublicKey(keyStr, leaderPodId, value)
				if err != nil {
					logDev("Failed to save member public key: %v", err)
					continue
				}
			}
		}
	}
}

// save the re-encryption key of a member from etcd
func (kr *KeyRegistry) HandleMemberReEncryptionKey(keyStr, leaderPodId string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - HandleMemberReEncryptionKey")

	rks := new(samba.ReEncryptionKeySerialized)
	err := json.NewDecoder(bytes.NewReader(value)).Decode(rks)
	if err != nil {
		return fmt.Errorf("failed to decode re-encryption key: %v", err)
	}

	reEncryptionKey, err := rks.DeSerialize()
	if err != nil {
		return fmt.Errorf("failed to deserialize re-encryption key: %v", err)
	}

	mlrKeyMap := kr.MemLeaderReEncryptionKey
	if mlrKeyMap == nil {
		mlrKeyMap = make(map[string]*pre.ReEncryptionKey)
	}
	mlrKeyMap[leaderPodId] = reEncryptionKey
	kr.SafeWriteMemLeaderReEncryptionKey(leaderPodId, reEncryptionKey)
	logDev("Got re-encryption key from leader %s", leaderPodId)

	// if I'm a member I'm ready for proxy re-encryption
	// as soon as I have the re-encryption key from the leader
	go kr.MarkPodPreReady()

	return nil
}

// get the public key of a member from etcd
// create the re-encryption key for the member
// store the re-encryption key in etcd
func (kr *KeyRegistry) HandleMemberPublicKey(keyStr, leaderPodId string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - HandleMemberPublicKey")

	pks := new(samba.PublicKeySerialized)
	err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
	if err != nil {
		return fmt.Errorf("failed to decode public key: %v", err)
	}

	publicKey, err := pks.DeSerialize()
	if err != nil {
		return fmt.Errorf("failed to deserialize public key: %v", err)
	}

	memberPodId := ""
	// keyStr is of the form "members/<leader-pod-id>/publicKey/<member-pod-id>"
	parts := strings.Split(keyStr, "/")
	memberPodId = strings.TrimSpace(parts[len(parts)-1])

	if memberPodId != "" {
		lmMap := kr.LeaMemPublicKeys
		if lmMap == nil {
			lmMap = make(map[string]*pre.PublicKey)
		}
		lmMap[memberPodId] = publicKey
		logDev("Got public key for member %s", memberPodId)

		// create a re-encryption key for the member
		pp, kp := kr.SafeReadLeaderKeys()
		reEncryptionKey := pre.ReEncryptionKeyGen(pp, kp.SK, publicKey)

		rKeyMap := kr.LeaMemReEncryptionKeys
		if rKeyMap == nil {
			rKeyMap = make(map[string]*pre.ReEncryptionKey)
		}
		rKeyMap[memberPodId] = reEncryptionKey
		logDev("Created re-encryption key for member %s", memberPodId)

		// member's re-encryption key is stored under
		// members/<leader-pod-id>/reEncryptionKey/<member-pod-id>
		memReEncKeyLabel := "members/" + leaderPodId + "/reEncryptionKey/" + memberPodId
		err = kr.StoreReEncryptionKey(memReEncKeyLabel, reEncryptionKey)
		if err != nil {
			return fmt.Errorf("failed to store member re-encryption key: %v", err)
		}
	} else {
		return fmt.Errorf("member pod ID not found in key: %s", keyStr)
	}

	return nil
}

// fetch public keys of all leaders from etcd
func (kr *KeyRegistry) FetchEveryExistingLeaderPublicKeys(allLeadersPublicKeyDir string) int64 {
	logDev := mutil.LogWithPrefix("dev - FetchEveryExistingLeaderPublicKeys")

	pubKeys, err := kr.Client().Get(context.Background(), allLeadersPublicKeyDir, clientv3.WithPrefix())
	if err != nil {
		logDev("failed to fetch existing leader public keys from etcd: %v", err)
		return -1
	}

	logDev("fetched %d leader public keys from etcd", len(pubKeys.Kvs))
	for _, kv := range pubKeys.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)
		keyStr := string(key)

		if strings.Contains(keyStr, "/publicKey/") {
			err := kr.HandleEveryLeaderPublicKey(keyStr, value)
			if err != nil {
				logDev("Failed to save leader public key: %v", err)
				continue
			}
		}

		if strings.Contains(keyStr, "/publicParams/") {
			err := kr.HandleEveryLeaderPublicParams(keyStr, value)
			if err != nil {
				logDev("Failed to save leader public params: %v", err)
				continue
			}
		}
	}

	logDev("current revision is %d", pubKeys.Header.Revision)
	return pubKeys.Header.Revision
}

// every function needs to watch for public keys and public params of all leaders
// keyPrefix is "leaders/"
func (kr *KeyRegistry) ListWatchEveryLeaderPublicKeys(keyPrefix string) {
	logDev := mutil.LogWithPrefix("dev - ListWatchEveryLeaderPublicKeys")

	currentRevision := kr.FetchEveryExistingLeaderPublicKeys(keyPrefix)
	if currentRevision < 0 {
		logDev("Failed to fetch existing leader public keys, cannot start watch")
		return
	}

	rch := kr.Client().Watch(context.Background(), keyPrefix, clientv3.WithPrefix(), clientv3.WithRev(currentRevision+1))
	logDev("Watching etcd keys with prefix: %s, rev: %d", keyPrefix, currentRevision+1)
	for wresp := range rch {
		if wresp.Canceled {
			logDev("etcd watch canceled: %v", wresp.Err())
			return
		}

		logDev("etcd watch response: %+v", wresp)
		for _, ev := range wresp.Events {
			logDev("type: %s, key: %q\n", ev.Type, ev.Kv.Key)

			if ev.Type == clientv3.EventTypePut {
				key := string(ev.Kv.Key)
				value := ev.Kv.Value

				logDev("Received PUT event for key: %s, value: %s", key, value)
				keyStr := string(key)

				if strings.Contains(keyStr, "/publicKey/") {
					err := kr.HandleEveryLeaderPublicKey(keyStr, value)
					if err != nil {
						logDev("Failed to save leader public key: %v", err)
						continue
					}
				}

				if strings.Contains(keyStr, "/publicParams/") {
					err := kr.HandleEveryLeaderPublicParams(keyStr, value)
					if err != nil {
						logDev("Failed to save leader public params: %v", err)
						continue
					}
				}
			}
		}
	}
}

func (kr *KeyRegistry) HandleEveryLeaderPublicParams(keyStr string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - HandleEveryLeaderPublicParams")

	pps := new(samba.PublicParamsSerialized)
	err := json.NewDecoder(bytes.NewReader(value)).Decode(pps)
	if err != nil {
		return fmt.Errorf("failed to decode public params: %v", err)
	}

	publicParams, err := pps.DeSerialize()
	if err != nil {
		return fmt.Errorf("failed to deserialize public param: %v", err)
	}

	keyStrParts := strings.Split(keyStr, "/")
	// leaders/<service-name>/<function-revision-name>/publicParams/<leader-pod-id>
	leaderServiceName := keyStrParts[1]

	kr.SafeWriteEveryLeaderPublicParams(leaderServiceName, publicParams)
	logDev("Got public params for leader of service %s", leaderServiceName)
	return nil
}

func (kr *KeyRegistry) HandleEveryLeaderPublicKey(keyStr string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - HandleEveryLeaderPublicKey")

	pks := new(samba.PublicKeySerialized)
	err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
	if err != nil {
		return fmt.Errorf("failed to decode public key: %v", err)
	}

	publicKey, err := pks.DeSerialize()
	if err != nil {
		return fmt.Errorf("failed to deserialize public key: %v", err)
	}

	keyStrParts := strings.Split(keyStr, "/")
	// leaders/<service-name>/<function-revision-name>/publicKey/<leader-pod-id>
	leaderServiceName := keyStrParts[1]

	kr.SafeWriteEveryLeaderPublicKey(leaderServiceName, publicKey)
	logDev("Got public key for leader of service %s", leaderServiceName)
	return nil
}

// a member function needs to watch for public params and public keys from the leader
// keyPrefix is "leaders/<service-name>/<function-revision>/public"
func (kr *KeyRegistry) ListWatchLeaderKeys(keyPrefix, leaderPodId string) {
	logDev := mutil.LogWithPrefix("dev - ListWatchLeaderKeys")

	currentRevision := kr.FetchExistingLeaderKeys(keyPrefix, leaderPodId)
	if currentRevision < 0 {
		logDev("Failed to fetch existing leader keys, cannot start watch")
		return
	}

	rch := kr.Client().Watch(context.Background(), keyPrefix, clientv3.WithPrefix(), clientv3.WithRev(currentRevision+1))
	logDev("Watching etcd keys with prefix: %s, rev: %d", keyPrefix, currentRevision+1)
	for wresp := range rch {
		if wresp.Canceled {
			logDev("etcd watch canceled: %v", wresp.Err())
			return
		}

		logDev("etcd watch response: %+v", wresp)
		for _, ev := range wresp.Events {
			logDev("type: %s, key: %q\n", ev.Type, ev.Kv.Key)

			if ev.Type == clientv3.EventTypePut {
				key := string(ev.Kv.Key)
				value := ev.Kv.Value

				logDev("Received PUT event for key: %s, value: %s", key, value)
				keyStr := string(key)

				err := kr.HandleLeaderKeys(keyStr, leaderPodId, value)
				if err != nil {
					logDev("Failed to save leader public keys: %v", err)
					continue
				}
			}
		}
	}
}

// return the current revision
func (kr *KeyRegistry) FetchExistingLeaderKeys(keyPrefix, leaderPodId string) int64 {
	logDev := mutil.LogWithPrefix("dev - FetchExistingLeaderKeys")

	getRes, err := kr.Client().Get(context.Background(), keyPrefix, clientv3.WithPrefix())
	if err != nil {
		logDev("failed to fetch existing leader's public keys from etcd: %v", err)
		return -1
	}

	logDev("fetched %d existing public keys from etcd", len(getRes.Kvs))
	for _, kv := range getRes.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)
		keyStr := string(key)

		err := kr.HandleLeaderKeys(keyStr, leaderPodId, value)
		if err != nil {
			logDev("Failed to save leader public keys: %v", err)
			continue
		}
	}

	logDev("current revision is %d", getRes.Header.Revision)
	return getRes.Header.Revision
}

// save the leader's pk,pp to local state
// generate new key pair for the member using pp
// send the member's public key to etcd
func (kr *KeyRegistry) HandleLeaderKeys(keyStr, leaderPodId string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - HandleLeaderKeys")

	if strings.HasSuffix(keyStr, "publicKey/"+leaderPodId) {
		pks := new(samba.PublicKeySerialized)
		err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
		if err != nil {
			return fmt.Errorf("failed to decode public key: %v", err)
		}

		publicKey, err := pks.DeSerialize()
		if err != nil {
			return fmt.Errorf("failed to deserialize public key: %v", err)
		}

		kr.SafeWriteMemLeaderPublicKey(leaderPodId, publicKey)
		logDev("Got public key for leader %s", leaderPodId)
		return nil
	}

	if strings.HasSuffix(keyStr, "publicParams/"+leaderPodId) {
		pks := new(samba.PublicParamsSerialized)
		err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
		if err != nil {
			return fmt.Errorf("failed to decode public params: %v", err)
		}

		publicParams, err := pks.DeSerialize()
		if err != nil {
			return fmt.Errorf("failed to deserialize public params: %v", err)
		}

		kr.SafeWriteMemLeaderPublicParams(leaderPodId, publicParams)
		logDev("Got public params for leader %s", leaderPodId)

		var keyPair *pre.KeyPair

		// instead of generating a new key pair for the member
		// read a static key pair from environment variable
		memberKeyPairString := os.Getenv("MEMBER_KP")
		keyPair, err = samba.ParseKeyPair([]byte(memberKeyPairString))
		if err != nil {
			logDev("Failed to parse static member key pair: %v", err)
		} else {
			logDev("Parsed static member key pair successfully")
		}

		if keyPair == nil {
			logDev("Generating new key pair for member using leader's public params")
			keyPair = pre.KeyGen(publicParams)
		}

		kr.SafeWriteMemKeyPair(leaderPodId, keyPair)
		logDev("Created key pair for member %s", kr.PodId)

		// a member stores their public key under a specific label
		// members/<leader-pod-id>/publicKey/<member-pod-id>
		memPubKeyLabel := "members/" + leaderPodId + "/publicKey/" + kr.PodId
		err = kr.StorePublicKey(memPubKeyLabel, keyPair.PK)
		if err != nil {
			return fmt.Errorf("failed to store member public key: %v", err)
		}

		return nil
	}

	return nil
}

func (kr *KeyRegistry) FetchStaticFunctionChains() {
	logDev := mutil.LogWithPrefix("dev - FetchStaticFunctionChains")

	// TODO: how do I know the function chain a request belongs to?
	funChainKey := "functionChainStatic/0"
	getRes, err := kr.Client().Get(context.Background(), funChainKey, clientv3.WithPrefix())
	if err != nil {
		logDev("failed to fetch function chain with key:%s from etcd: %v", funChainKey, err)
		return
	}

	logDev("fetched %d existing function chains from etcd", len(getRes.Kvs))
	for _, kv := range getRes.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)

		if kr.functionChains == nil {
			kr.functionChains = make([]string, 0)
		}
		kr.functionChains = append(kr.functionChains, string(key))
		//
		if kr.functionChainServices == nil {
			kr.functionChainServices = make(map[string]string)
		}
		kr.functionChainServices[string(key)] = string(value)
	}
}

func (kr *KeyRegistry) GetFunctionChainFromEnv() []string {
	logDev := mutil.LogWithPrefix("dev - GetFunctionChainFromEnv")
	functionChain := os.Getenv("FUNCTION_CHAIN")
	logDev("FUNCTION_CHAIN from env: %s", functionChain)
	if functionChain == "" {
		return []string{}
	}
	return strings.Split(functionChain, "/")
}

func (kr *KeyRegistry) GetDefaultFunctionChain() []string {
	if len(kr.functionChains) == 0 {
		// if there are no function chains, return an empty slice
		return []string{}
	}
	recentChain := kr.functionChains[len(kr.functionChains)-1]
	servicesStr := kr.functionChainServices[recentChain]
	return strings.Split(servicesStr, "/")
}

// encrypts the response received from user-container
func (kr *KeyRegistry) EncryptResponseBody(resp *http.Response) error {
	logDev := mutil.LogWithPrefix("dev - EncryptResponseBody")

	logDev("Response: %s %s %d\n", resp.Request.Method, resp.Request.URL.String(), resp.StatusCode)

	contentTypeStr := resp.Header.Get("Content-Type")
	logDev("Response Content-Type: %s", contentTypeStr)

	for name, values := range resp.Header {
		for _, value := range values {
			logDev("  %s: %s\n", name, value)
		}
	}

	nonce := resp.Header.Get("Ce-Nonce")

	// TODO: maybe implement streaming read/encryption?
	plainBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		logDev("Error reading response body: %v", err)
		return err
	}
	resp.Body.Close()
	logDev("Response Body (plain) (from user-container): %s", string(plainBytes))

	var encryptedBytes []byte
	if len(plainBytes) == 0 {
		logDev("Response body is empty, nothing to encrypt.")
	} else {
		logDev("Response Body length: %d", len(plainBytes))
		functionMode := mutil.GetFunctionMode()

		if functionMode == mutil.FunctionModeEmpty {
			logDev("Function mode is undefined, skipping encryption.")
			encryptedBytes = plainBytes
		}

		if functionMode == mutil.FunctionModeFake {
			// fake-encrypt the response body using placeholder Encrypt function
			logDev("Function mode is FAKE, using FakeEncrypt method.")
			encryptedBytes, err = mutil.FakeEncrypt(plainBytes)
			if err != nil {
				logDev("Error encrypting response body: %v", err)
				return err
			}
		}

		if functionMode == mutil.FunctionModeSingle {
			logDev("Function mode is SINGLE")

			// prefer to use RSA secret key if available
			if kr.RSASecretKey != nil {
				logDev("Using RSA private key to encrypt message.")
				encryptedBytes, err = mutil.RSAEncrypt(&kr.RSASecretKey.PublicKey, plainBytes)
				if err != nil {
					log.Fatalf("failed to encrypt message using RSA private key: %v", err.Error())
				}
			} else {
				logDev("Using CLIENT_PP & CLIENT_PK to encrypt message.")
				targetName := "client"

				if kr.ClientPk == nil || kr.ClientPp == nil {
					return fmt.Errorf("client public key or public parameters are not set")
				}

				encryptedBytes, err = mutil.PreEncrypt(kr.ClientPp, kr.ClientPk, plainBytes, targetName)
				if err != nil {
					return fmt.Errorf("failed to get default message: %v", err.Error())
				}
			}
		}

		if functionMode == mutil.FunctionModeChain {
			chainedServices := kr.GetFunctionChainFromEnv()
			currentService := kr.ServiceName
			currentServiceIndex := slices.Index(chainedServices, currentService)
			nextServiceIndex := -1
			if currentServiceIndex >= 0 { // this can be -1
				nextServiceIndex = currentServiceIndex + 1
			}

			// later if we can, encrypt the response body for next service in the chain
			// encrypt request at first (going from first -> second), and at second (going from second -> third)
			if nextServiceIndex < 0 || nextServiceIndex > len(chainedServices) {
				logDev("No next service in the chain, skipping proxy encryption")
				encryptedBytes = plainBytes
			} else if nextServiceIndex == len(chainedServices) {
				logDev("We are at the end of the chain, no next service to encrypt for, so encrypting for client")
				logDev("Using CLIENT_PP & CLIENT_PK to encrypt message.")

				targetName := "client"

				if kr.ClientPk == nil || kr.ClientPp == nil {
					return fmt.Errorf("client public key or public parameters are not set")
				}

				encryptedBytes, err = mutil.PreEncrypt(kr.ClientPp, kr.ClientPk, plainBytes, targetName)
				if err != nil {
					return fmt.Errorf("failed to get default message: %v", err.Error())
				}
			} else {
				nextService := chainedServices[nextServiceIndex]
				logDev("Encrypting response body for next service %s in the chain: %s", nextService, chainedServices)

				nextServicePubKey := kr.SafeReadEveryLeaderPublicKey(nextService)
				nextServicePubParams := kr.SafeReadEveryLeaderPublicParams(nextService)

				encryptedBytes, err = mutil.PreEncrypt(nextServicePubParams, nextServicePubKey, plainBytes, nextService)
				if err != nil {
					return fmt.Errorf("failed to get default message: %v", err.Error())
				}
			}
		}

		logDev("Response Body (encrypted): %s", string(encryptedBytes))
		logDev("Response Body length: %d", len(encryptedBytes))
	}

	if nonce != "" {
		// read the updated agg-signature and function chain
		logDev("Response Ce-Nonce header found: %s", nonce)

		chain, aggSig := kr.GetAggSignatureAndChain(nonce)
		if chain == "" || aggSig == "" {
			logDev("agg-signature or function chain for nonce not found, setting to 'unknown'")
			chain = "unknown"
			aggSig = "unknown"
		}
		resp.Header.Set("Ce-Aggsignature", aggSig)
		resp.Header.Set("Ce-Functionchain", chain)
	}

	resp.Body = io.NopCloser(bytes.NewReader(encryptedBytes))
	resp.ContentLength = int64(len(encryptedBytes))
	if strings.Contains(contentTypeStr, "grpc") {
		resp.Header.Set("Content-Type", "application/grpc")
	} else {
		resp.Header.Set("Content-Type", "application/json")
	}
	resp.Header.Set("Content-Length", fmt.Sprint(len(encryptedBytes)))

	return nil
}
