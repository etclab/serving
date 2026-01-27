package kregistry

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/mutil"
)

// ============================================================
// Hash Chain Data Structures for Tamper-Evident Enclave Key Storage
// ============================================================

// HashChainHead represents the head of the audit log chain
type HashChainHead struct {
	Idx      uint64 `json:"idx"`
	Digest   []byte `json:"digest"`
	WriterID string `json:"writer_id"`
	HeadSig  []byte `json:"head_sig"`
}

// HashChainEntry represents an entry in the audit log
type HashChainEntry struct {
	Idx        uint64 `json:"idx"`
	PrevDigest []byte `json:"prev_digest"`
	OpType     string `json:"op_type"`
	DataKey    string `json:"data_key"`
	ValueHash  []byte `json:"value_hash"`
	WriterID   string `json:"writer_id"`
	EntrySig   []byte `json:"entry_sig"`
}

// HashChainDataRecord represents the signed data record for enclave public keys
type HashChainDataRecord struct {
	Idx      uint64 `json:"idx"`
	WriterID string `json:"writer_id"`
	Payload  []byte `json:"payload"`
	DataSig  []byte `json:"data_sig"`
}

// Hash chain key paths
const (
	HashChainHeadKey       = "lambada/audit/head"
	HashChainEntryPrefix   = "lambada/audit/entry/"
	EnclaveKeysPrefix      = "enclave-keys/"
	EnclavePublicKeySuffix = "/publicKey"
)

// ============================================================
// Hash Chain Helper Functions
// ============================================================

// computeHeadSignatureMessage creates the message to sign for the head record
// Format: H("head" || idx || writer_id || digest)
func computeHeadSignatureMessage(idx uint64, writerId string, digest []byte) []byte {
	buf := make([]byte, 0, 4+8+len(digest))
	buf = append(buf, []byte("head")...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, []byte(writerId)...)
	buf = append(buf, digest...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeEntrySignatureMessage creates the message to sign for an entry record
// Format: H("entry" || idx || prev_digest || op_type || data_key || value_hash || writer_id)
func computeEntrySignatureMessage(entry *HashChainEntry) []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("entry")...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, entry.Idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, entry.PrevDigest...)
	buf = append(buf, []byte(entry.OpType)...)
	buf = append(buf, []byte(entry.DataKey)...)
	buf = append(buf, entry.ValueHash...)
	buf = append(buf, []byte(entry.WriterID)...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeDataSignatureMessage creates the message to sign for a data record
// Format: H("data" || data_key || idx || H(payload) || writer_id)
func computeDataSignatureMessage(dataKey string, idx uint64, payloadHash []byte, writerID string) []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("data")...)
	buf = append(buf, []byte(dataKey)...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, payloadHash...)
	buf = append(buf, []byte(writerID)...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeNewDigest computes the chain digest: H(prev_digest || H(entry_fields) || H(entry_sig))
func computeNewDigest(prevDigest []byte, entry *HashChainEntry) []byte {
	// Hash the entry fields (without sig)
	entryFieldsHash := computeEntrySignatureMessage(entry)

	// Hash the signature
	sigHash := sha256.Sum256(entry.EntrySig)

	// Combine: H(prev_digest || H(entry_fields) || H(sig))
	buf := make([]byte, 0, len(prevDigest)+32+32)
	buf = append(buf, prevDigest...)
	buf = append(buf, entryFieldsHash...)
	buf = append(buf, sigHash[:]...)

	hash := sha256.Sum256(buf)
	return hash[:]
}

// ============================================================
// Hash Chain Methods on KeyRegistry
// ============================================================

// GetHashChainHead retrieves the current hash chain head from etcd
func (kr *KeyRegistry) GetHashChainHead(ctx context.Context) (*HashChainHead, int64, error) {
	logDev := mutil.LogWithPrefix("dev - GetHashChainHead")

	resp, err := kr.Client().Get(ctx, HashChainHeadKey)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get hash chain head: %w", err)
	}

	if len(resp.Kvs) == 0 {
		logDev("Hash chain head does not exist yet")
		return nil, 0, nil // Head doesn't exist yet
	}

	var head HashChainHead
	if err := json.Unmarshal(resp.Kvs[0].Value, &head); err != nil {
		return nil, 0, fmt.Errorf("failed to unmarshal hash chain head: %w", err)
	}

	logDev("Retrieved hash chain head: idx=%d, digest=%x", head.Idx, head.Digest)
	// TODO: instead of using ModRevision use the Idx from head?
	return &head, resp.Kvs[0].ModRevision, nil
}

// TODO: this should use the public key of respective function
// VerifyHeadSignature verifies the signature on the hash chain head
func VerifyHeadSignature(head *HashChainHead, clientPubKey ed25519.PublicKey) error {
	if clientPubKey == nil {
		return fmt.Errorf("client public key is nil")
	}

	msg := computeHeadSignatureMessage(head.Idx, head.WriterID, head.Digest)
	if !ed25519.Verify(clientPubKey, msg, head.HeadSig) {
		return fmt.Errorf("head signature verification failed")
	}
	return nil
}

// executeHashChainTransaction atomically writes data, entry, and head to etcd
func (kr *KeyRegistry) executeHashChainTransaction(
	ctx context.Context,
	dataKey string,
	dataRecord *HashChainDataRecord,
	entry *HashChainEntry,
	newHead *HashChainHead,
	headModRev int64,
	isFirstWrite bool,
) (bool, error) {
	logDev := mutil.LogWithPrefix("dev - executeHashChainTransaction")

	dataRecordBytes, err := json.Marshal(dataRecord)
	if err != nil {
		return false, fmt.Errorf("failed to marshal data record: %w", err)
	}

	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return false, fmt.Errorf("failed to marshal entry record: %w", err)
	}

	headBytes, err := json.Marshal(newHead)
	if err != nil {
		return false, fmt.Errorf("failed to marshal head record: %w", err)
	}

	entryKey := HashChainEntryPrefix + strconv.FormatUint(entry.Idx, 10)

	var txnResp *clientv3.TxnResponse

	if isFirstWrite {
		// First write: head doesn't exist yet
		logDev("Executing first write transaction (creating head)")
		txnResp, err = kr.Client().Txn(ctx).If(
			clientv3.Compare(clientv3.CreateRevision(HashChainHeadKey), "=", 0), // Head doesn't exist
			clientv3.Compare(clientv3.CreateRevision(dataKey), "=", 0),          // Write-once: data key doesn't exist
			clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),         // Append-only: entry doesn't exist
		).Then(
			clientv3.OpPut(dataKey, string(dataRecordBytes)),
			clientv3.OpPut(entryKey, string(entryBytes)),
			clientv3.OpPut(HashChainHeadKey, string(headBytes)),
		).Commit()
	} else {
		// Subsequent write: check head hasn't changed
		logDev("Executing subsequent write transaction (updating head)")
		txnResp, err = kr.Client().Txn(ctx).If(
			clientv3.Compare(clientv3.ModRevision(HashChainHeadKey), "=", headModRev), // Head unchanged
			clientv3.Compare(clientv3.CreateRevision(dataKey), "=", 0),                // Write-once: data key doesn't exist
			clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),               // Append-only: entry doesn't exist
		).Then(
			clientv3.OpPut(dataKey, string(dataRecordBytes)),
			clientv3.OpPut(entryKey, string(entryBytes)),
			clientv3.OpPut(HashChainHeadKey, string(headBytes)),
		).Commit()
	}

	if err != nil {
		return false, fmt.Errorf("transaction failed: %w", err)
	}

	if !txnResp.Succeeded {
		logDev("Transaction conflict detected, will retry")
		return false, nil
	}

	logDev("Transaction succeeded: dataKey=%s, entryIdx=%d", dataKey, entry.Idx)
	return true, nil
}

// StoreEnclavePublicKeyWithHashChain stores an enclave public key with hash chain integrity
func (kr *KeyRegistry) StoreEnclavePublicKeyWithHashChain(
	ctx context.Context,
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	clientPubKey ed25519.PublicKey,
	genesisHash []byte,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreEnclavePublicKeyWithHashChain")

	if enclavePubKey == nil || enclavePrivKey == nil {
		return fmt.Errorf("enclave keypair is nil")
	}

	dataKey := EnclaveKeysPrefix + podID + EnclavePublicKeySuffix
	writerID := podID

	// Get current head
	head, headModRev, err := kr.GetHashChainHead(ctx)
	if err != nil {
		return fmt.Errorf("failed to get hash chain head: %w", err)
	}

	var idx uint64
	var prevDigest []byte
	isFirstWrite := false

	if head == nil {
		// First write: use genesis hash
		if len(genesisHash) == 0 {
			logDev("Warning: No genesis hash provided for first write, using zero hash")
			prevDigest = make([]byte, 32)
		} else {
			prevDigest = genesisHash
		}
		idx = 1
		isFirstWrite = true
		// TODO: if genesis hash exists then it's already verified
		// TODO: genesis hash isn't stored unless it is verified
		logDev("First write: using genesis hash as prev_digest")
	} else {
		// Verify existing head signature
		// TODO: we verify the head signature using the writer's public key
		// TODO: this means we need the function which updated the head last time
		// TODO: in order to verify the head signature
		if clientPubKey != nil {
			if err := VerifyHeadSignature(head, clientPubKey); err != nil {
				return fmt.Errorf("existing head signature verification failed (tampering detected): %w", err)
			}
			logDev("Existing head signature verified successfully")
		} else {
			logDev("Warning: No client public key available, skipping head signature verification")
		}
		prevDigest = head.Digest
		idx = head.Idx + 1
		logDev("Subsequent write: chaining from idx=%d", head.Idx)
	}

	// Compute value hash (hash of the public key payload)
	payloadHash := sha256.Sum256(enclavePubKey)

	// Create and sign data record
	dataSignMsg := computeDataSignatureMessage(dataKey, idx, payloadHash[:], writerID)
	dataSig := ed25519.Sign(enclavePrivKey, dataSignMsg)

	dataRecord := &HashChainDataRecord{
		Idx:      idx,
		WriterID: writerID,
		Payload:  enclavePubKey,
		DataSig:  dataSig,
	}

	// Create entry record (without signature first to compute signature message)
	entry := &HashChainEntry{
		Idx:        idx,
		PrevDigest: prevDigest,
		OpType:     "PUT",
		DataKey:    dataKey,
		ValueHash:  payloadHash[:],
		WriterID:   writerID,
	}

	// Sign the entry
	entrySignMsg := computeEntrySignatureMessage(entry)
	entry.EntrySig = ed25519.Sign(enclavePrivKey, entrySignMsg)

	// Compute new digest
	// TODO: can reuse the hash and signature from above
	newDigest := computeNewDigest(prevDigest, entry)

	// Create and sign new head
	headSignMsg := computeHeadSignatureMessage(idx, writerID, newDigest)
	headSig := ed25519.Sign(enclavePrivKey, headSignMsg)

	newHead := &HashChainHead{
		Idx:      idx,
		Digest:   newDigest,
		WriterID: writerID,
		HeadSig:  headSig,
	}

	// Execute atomic transaction
	success, err := kr.executeHashChainTransaction(ctx, dataKey, dataRecord, entry, newHead, headModRev, isFirstWrite)
	if err != nil {
		return err
	}

	if !success {
		return fmt.Errorf("transaction conflict: head or data key changed")
	}

	logDev("Successfully stored enclave public key with hash chain: podID=%s, idx=%d", podID, idx)
	return nil
}

// StoreEnclavePublicKeyWithRetry stores an enclave public key with hash chain, retrying on conflicts
func (kr *KeyRegistry) StoreEnclavePublicKeyWithRetry(
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	clientPubKey ed25519.PublicKey,
	genesisHash []byte,
	maxRetries int,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreEnclavePublicKeyWithRetry")

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := kr.StoreEnclavePublicKeyWithHashChain(ctx, podID, enclavePubKey, enclavePrivKey, clientPubKey, genesisHash)
		cancel()

		if err == nil {
			logDev("Successfully stored enclave public key on attempt %d", attempt+1)
			return nil
		}

		// TODO: revisit transaction conflict error detection
		// Check if it's a conflict error (retryable)
		if strings.Contains(err.Error(), "transaction conflict") {
			logDev("Transaction conflict on attempt %d, retrying after %v", attempt+1, backoff)
			time.Sleep(backoff)
			backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
			continue
		}

		// Check if key already exists (write-once violation - not retryable)
		if strings.Contains(err.Error(), "already exists") {
			logDev("Key already exists, write-once constraint enforced")
			return err
		}
		// TODO: revisit transaction conflict error detection

		// Other errors
		logDev("Error on attempt %d: %v", attempt+1, err)
		return err
	}

	return fmt.Errorf("failed to store enclave public key after %d attempts", maxRetries)
}

// TODO: need to think more on how to verify the enclave public key
// TODO: where is this being used? -> nowhere
// GetEnclavePublicKeyFromHashChain retrieves and verifies an enclave public key from the hash chain
func (kr *KeyRegistry) GetEnclavePublicKeyFromHashChain(
	ctx context.Context,
	podID string,
	clientPubKey ed25519.PublicKey,
) (ed25519.PublicKey, error) {
	logDev := mutil.LogWithPrefix("dev - GetEnclavePublicKeyFromHashChain")

	dataKey := EnclaveKeysPrefix + podID + EnclavePublicKeySuffix

	resp, err := kr.Client().Get(ctx, dataKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get enclave public key: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("enclave public key not found for pod %s", podID)
	}

	var dataRecord HashChainDataRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &dataRecord); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data record: %w", err)
	}

	// Verify data signature
	payloadHash := sha256.Sum256(dataRecord.Payload)
	dataSignMsg := computeDataSignatureMessage(dataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)

	// Get the enclave public key from the payload to verify the signature
	if len(dataRecord.Payload) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid payload size: expected %d, got %d", ed25519.PublicKeySize, len(dataRecord.Payload))
	}

	enclavePubKey := ed25519.PublicKey(dataRecord.Payload)
	if !ed25519.Verify(enclavePubKey, dataSignMsg, dataRecord.DataSig) {
		return nil, fmt.Errorf("data signature verification failed (tampering detected)")
	}

	logDev("Successfully verified enclave public key for pod %s at idx %d", podID, dataRecord.Idx)
	return enclavePubKey, nil
}
