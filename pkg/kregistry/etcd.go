package kregistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/etclab/pre"
	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"
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
	client      *clientv3.Client
	IsEtcdReady chan struct{}

	// InstanceId is the ip address of the pod
	InstanceId string
	// RevisionId is the unique id for the function
	// and identifies a deployed revision of the function
	FunctionId string
	// PodId is the unique id for the pod
	PodId        string
	KeyPair      *pre.KeyPair
	PublicParams *pre.PublicParams
	// Crypto       SambaCrypto

	// state when queue-proxy is a leader
	LeaPublicParams *pre.PublicParams
	LeaKeyPair      *pre.KeyPair

	// state when queue-proxy is a member
	// TODO: I think the best way to store these values is to mimic the
	// path (or folder) like structure in etcd using maps
	MemKeyPair            *pre.KeyPair
	MemLeaderIds          []string
	MemLeaderPublicKey    map[string]*pre.PublicKey
	MemLeaderPublicParams map[string]*pre.PublicParams
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

// other functions I imagine would be
// watchMemberPublicKeys - leader watches this for public keys from members
// watchReEncryptionKeys - member watches this for re-encryption keys from the leader
// TODO: how do I cancel this watch? after leader re-election

// a member function needs to watch for public params and public keys from the leader
// keyPrefix is "leaders/<function-revision>/public"
func (kr *KeyRegistry) WatchLeaderKeys(keyPrefix, identity string) {
	logDev := mutil.LogWithPrefix("dev - watchLeaderKeys")

	rch := kr.Client().Watch(context.Background(), keyPrefix, clientv3.WithPrefix())
	logDev("Watching etcd keys with prefix: %s", keyPrefix)
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

				err := kr.GetLeaderKeys(keyStr, identity, value)
				if err != nil {
					logDev("Failed to save leader public keys: %v", err)
					continue
				}
			}
		}
	}
}

func (kr *KeyRegistry) FetchExistingLeaderKeys(keyPrefix, identity string) {
	logDev := mutil.LogWithPrefix("dev - FetchExistingLeaderKeys")

	getRes, err := kr.Client().Get(context.Background(), keyPrefix, clientv3.WithPrefix())
	if err != nil {
		logDev("failed to fetch existing leader's public keys from etcd: %v", err)
		return
	}

	logDev("fetched %d existing public keys from etcd", len(getRes.Kvs))
	for _, kv := range getRes.Kvs {
		value := kv.Value
		key := kv.Key

		logDev("Received key: %s, value: %s", key, value)
		keyStr := string(key)

		err := kr.GetLeaderKeys(keyStr, identity, value)
		if err != nil {
			logDev("Failed to save leader public keys: %v", err)
			continue
		}
	}
}

func (kr *KeyRegistry) GetLeaderKeys(keyStr, identity string, value []byte) error {
	logDev := mutil.LogWithPrefix("dev - GetLeaderKeys")

	if strings.HasSuffix(keyStr, "publicKey") {
		pks := new(samba.PublicKeySerialized)
		err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
		if err != nil {
			return fmt.Errorf("failed to decode public key: %v", err)
		}

		publicKey, err := pks.DeSerialize()
		if err != nil {
			return fmt.Errorf("failed to deserialize public key: %v", err)
		}

		pkMap := kr.MemLeaderPublicKey
		if pkMap == nil {
			pkMap = make(map[string]*pre.PublicKey)
		}
		pkMap[identity] = publicKey
		logDev("Got public key for leader %s", identity)
		return nil
	}

	if strings.HasSuffix(keyStr, "publicParams") {
		pks := new(samba.PublicParamsSerialized)
		err := json.NewDecoder(bytes.NewReader(value)).Decode(pks)
		if err != nil {
			return fmt.Errorf("failed to decode public params: %v", err)
		}

		publicParams, err := pks.DeSerialize()
		if err != nil {
			return fmt.Errorf("failed to deserialize public params: %v", err)
		}

		ppMap := kr.MemLeaderPublicParams
		if ppMap == nil {
			ppMap = make(map[string]*pre.PublicParams)
		}
		ppMap[identity] = publicParams
		logDev("Got public params for leader %s", identity)

		kr.MemKeyPair = pre.KeyGen(publicParams)
		logDev("Created key pair for member %s", kr.PodId)

		// a member stores their public key under a specific label
		// members/<leader-pod-id>/<member-pod-id>/publicKey
		memPubKeyLabel := "members/" + identity + "/" + kr.PodId + "/publicKey"
		err = kr.StorePublicKey(memPubKeyLabel, kr.MemKeyPair.PK)
		if err != nil {
			logDev("Failed to store member public key: %v", err)
		}

		return nil
	}

	return nil
}
