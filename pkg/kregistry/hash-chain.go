package kregistry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/edgelesssys/ego/attestation"
	"github.com/edgelesssys/ego/ecrypto"
	"github.com/edgelesssys/ego/enclave"
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

// AttestedPublicKey packs the enclave public key and its attestation report together.
// This is stored as the Payload in HashChainDataRecord.
type AttestedPublicKey struct {
	PublicKey   []byte `json:"public_key"`
	Attestation []byte `json:"attestation"`
}

// Hash chain key paths
const (
	HashChainHeadKey       = "lambada/audit/head"
	HashChainEntryPrefix   = "lambada/audit/entry/"
	EnclaveKeysPrefix      = "enclave-keys/"
	EnclavePublicKeySuffix = "/attested-publicKey"
)

// LocalVerifiedState tracks the last verified chain state (in-memory).
// This allows incremental verification for multiple writes from the same pod.
// State is lost on restart, requiring re-verification from genesis.
type LocalVerifiedState struct {
	mu             sync.RWMutex
	verifiedIdx    uint64
	verifiedDigest []byte
}

// Package-level local state for chain verification caching
var localState = &LocalVerifiedState{}

// Constants for sealed state storage
const (
	SealedStateDir = "/sealed-state"
)

// SealedVerifiedState is the struct serialized for sealed storage
type SealedVerifiedState struct {
	VerifiedIdx    uint64 `json:"verified_idx"`
	VerifiedDigest []byte `json:"verified_digest"`
}

// sealVerifiedState persists the verified state to disk using EGO sealing.
// The state is sealed with the enclave's product key, allowing it to survive
// enclave restarts as long as the signing key remains the same.
func sealVerifiedState(podID string, idx uint64, digest []byte) error {
	logDev := mutil.LogWithPrefix("dev - sealVerifiedState")

	state := SealedVerifiedState{
		VerifiedIdx:    idx,
		VerifiedDigest: digest,
	}

	plaintext, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal verified state: %w", err)
	}

	// Use podID as additional data to bind the sealed data to this pod
	additionalData := []byte(podID)

	sealed, err := ecrypto.SealWithProductKey(plaintext, additionalData)
	if err != nil {
		return fmt.Errorf("failed to seal verified state: %w", err)
	}

	// Ensure the sealed state directory exists
	if err := os.MkdirAll(SealedStateDir, 0700); err != nil {
		return fmt.Errorf("failed to create sealed state directory: %w", err)
	}

	filePath := filepath.Join(SealedStateDir, podID+".sealed")
	if err := os.WriteFile(filePath, sealed, 0600); err != nil {
		return fmt.Errorf("failed to write sealed state file: %w", err)
	}

	logDev("Sealed verified state: idx=%d, file=%s", idx, filePath)
	return nil
}

// unsealVerifiedState loads the verified state from sealed storage.
// Returns (0, nil, nil) if no sealed state exists (fresh start).
func unsealVerifiedState(podID string) (uint64, []byte, error) {
	logDev := mutil.LogWithPrefix("dev - unsealVerifiedState")

	filePath := filepath.Join(SealedStateDir, podID+".sealed")

	sealed, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			logDev("No sealed state file found for pod %s (fresh start)", podID)
			return 0, nil, nil
		}
		return 0, nil, fmt.Errorf("failed to read sealed state file: %w", err)
	}

	// Use podID as additional data (must match what was used during sealing)
	additionalData := []byte(podID)

	plaintext, err := ecrypto.Unseal(sealed, additionalData)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to unseal verified state: %w", err)
	}

	var state SealedVerifiedState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return 0, nil, fmt.Errorf("failed to unmarshal verified state: %w", err)
	}

	logDev("Unsealed verified state: idx=%d, digest=%x", state.VerifiedIdx, state.VerifiedDigest)
	return state.VerifiedIdx, state.VerifiedDigest, nil
}

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
func computeNewDigest(prevDigest []byte, entryFieldsHash []byte, entry *HashChainEntry) []byte {
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
	return &head, resp.Kvs[0].ModRevision, nil
}

// VerifyHeadSignature verifies the signature on the hash chain head
func VerifyHeadSignature(head *HashChainHead, writerPubKey ed25519.PublicKey) error {
	if writerPubKey == nil {
		return fmt.Errorf("writer public key is nil")
	}

	msg := computeHeadSignatureMessage(head.Idx, head.WriterID, head.Digest)
	if !ed25519.Verify(writerPubKey, msg, head.HeadSig) {
		return fmt.Errorf("head signature verification failed")
	}
	return nil
}

// verifyEntrySignature verifies the signature on a hash chain entry
func verifyEntrySignature(entry *HashChainEntry, writerPubKey ed25519.PublicKey) error {
	if writerPubKey == nil {
		return fmt.Errorf("writer public key is nil")
	}

	msg := computeEntrySignatureMessage(entry)
	if !ed25519.Verify(writerPubKey, msg, entry.EntrySig) {
		return fmt.Errorf("entry signature verification failed at idx %d", entry.Idx)
	}
	return nil
}

// getEntry retrieves a hash chain entry from etcd by its key
func (kr *KeyRegistry) getEntry(ctx context.Context, entryKey string) (*HashChainEntry, error) {
	resp, err := kr.Client().Get(ctx, entryKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get entry: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("entry not found: %s", entryKey)
	}

	var entry HashChainEntry
	if err := json.Unmarshal(resp.Kvs[0].Value, &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal entry: %w", err)
	}
	return &entry, nil
}

// getWriterPublicKey retrieves a writer's public key from etcd and verifies its attestation.
// The public key is stored in enclave-keys/<writerID>/attested-publicKey as an AttestedPublicKey.
//
// Trust chain:
// 1. Verify attestation report (Intel's root of trust)
// 2. Verify SHA256(writerID || publicKey) matches report.Data
// 3. Trust the public key
// 4. Verify data signature for integrity
// 5. Use trusted public key for hash chain signature verification
func (kr *KeyRegistry) getWriterPublicKey(ctx context.Context, writerID string) (ed25519.PublicKey, error) {
	logDev := mutil.LogWithPrefix("dev - getWriterPublicKey")

	dataKey := EnclaveKeysPrefix + writerID + EnclavePublicKeySuffix
	resp, err := kr.Client().Get(ctx, dataKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get writer public key: %w", err)
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("writer public key not found for %s", writerID)
	}

	var dataRecord HashChainDataRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &dataRecord); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data record: %w", err)
	}

	// Unpack the AttestedPublicKey from the payload
	var attestedKey AttestedPublicKey
	if err := json.Unmarshal(dataRecord.Payload, &attestedKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal attested public key: %w", err)
	}

	if len(attestedKey.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(attestedKey.PublicKey))
	}

	// Strict mode: fail if attestation report is empty/nil
	if len(attestedKey.Attestation) == 0 {
		return nil, fmt.Errorf("attestation report is empty for writerID %s", writerID)
	}

	// 1. Verify attestation report using Intel's root of trust
	report, err := enclave.VerifyRemoteReport(attestedKey.Attestation)
	if err != nil {
		if err == attestation.ErrTCBLevelInvalid {
			// TCB level invalid is acceptable - just means old microcode/firmware
			logDev("warning: TCB level invalid in attestation report for writerID %s", writerID)
		} else {
			return nil, fmt.Errorf("failed to verify attestation report for writerID %s: %w", writerID, err)
		}
	}

	// 2. Verify enclave properties (SignerID, ProductID, SecurityVersion)
	if err := mutil.VerifyReport(report); err != nil {
		return nil, fmt.Errorf("enclave verification failed for writerID %s: %w", writerID, err)
	}

	// 3. Verify report data binding: SHA256(writerID || publicKey) must match report.Data
	h := sha256.New()
	h.Write([]byte(writerID))
	h.Write(attestedKey.PublicKey)
	expectedReportData := h.Sum(nil)

	if !bytes.Equal(report.Data[:len(expectedReportData)], expectedReportData) {
		return nil, fmt.Errorf("attestation report data mismatch: public key not bound to writerID %s", writerID)
	}

	logDev("Attestation verified for writerID %s", writerID)

	// 4. Verify data signature for additional integrity
	pubKey := ed25519.PublicKey(attestedKey.PublicKey)
	payloadHash := sha256.Sum256(dataRecord.Payload)
	dataSignMsg := computeDataSignatureMessage(dataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)
	if !ed25519.Verify(pubKey, dataSignMsg, dataRecord.DataSig) {
		return nil, fmt.Errorf("data signature verification failed for writerID %s", writerID)
	}

	logDev("Data signature verified for writerID %s", writerID)

	// 5. Return the trusted public key
	return pubKey, nil
}

// advanceAndVerifyChain verifies the hash chain from local verified state to the current head.
// This is the core verification logic that ensures chain integrity before appending.
func (kr *KeyRegistry) advanceAndVerifyChain(
	ctx context.Context,
	genesisHash []byte,
	head *HashChainHead,
) error {
	logDev := mutil.LogWithPrefix("dev - advanceAndVerifyChain")

	// 1. Check local state for incremental verification
	localState.mu.RLock()
	startIdx := localState.verifiedIdx + 1
	prevDigest := localState.verifiedDigest
	localState.mu.RUnlock()

	// If no local state, try to load from sealed storage
	if startIdx == 1 || prevDigest == nil {
		if kr.PodId != "" {
			sealedIdx, sealedDigest, err := unsealVerifiedState(kr.PodId)
			if err != nil {
				logDev("Warning: failed to unseal state: %v", err)
			} else if sealedIdx > 0 && sealedDigest != nil {
				startIdx = sealedIdx + 1
				prevDigest = sealedDigest
				logDev("Loaded sealed state: idx=%d", sealedIdx)
			}
		}
	}

	// If still no state (no sealed state or fresh start), start from genesis
	if startIdx == 1 || prevDigest == nil {
		startIdx = 1
		if len(genesisHash) == 0 {
			logDev("Warning: No genesis hash provided, using zero hash")
			prevDigest = make([]byte, 32)
		} else {
			prevDigest = genesisHash
		}
		logDev("Starting chain verification from genesis")
	} else {
		logDev("Starting incremental chain verification from idx=%d", startIdx)
	}

	// TODO: bulk fetch entries at once from startIdx <= head.Idx
	// 2. Fetch and verify entries from startIdx to head.Idx
	for i := startIdx; i <= head.Idx; i++ {
		entryKey := HashChainEntryPrefix + strconv.FormatUint(i, 10)
		entry, err := kr.getEntry(ctx, entryKey)
		if err != nil {
			return fmt.Errorf("failed to fetch entry %d: %w", i, err)
		}

		// Verify entry.PrevDigest matches our computed prevDigest
		if !bytes.Equal(entry.PrevDigest, prevDigest) {
			return fmt.Errorf("chain broken at idx %d: prev_digest mismatch (expected %x, got %x)",
				i, prevDigest, entry.PrevDigest)
		}

		// Get writer's public key and verify entry signature
		writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
		if err != nil {
			return fmt.Errorf("failed to get writer public key for %s: %w", entry.WriterID, err)
		}
		if err := verifyEntrySignature(entry, writerPubKey); err != nil {
			return fmt.Errorf("entry verification failed at idx %d: %w", i, err)
		}

		// Compute new digest for next iteration
		entrySignHash := computeEntrySignatureMessage(entry)
		prevDigest = computeNewDigest(prevDigest, entrySignHash, entry)
		logDev("Verified entry idx=%d, writer=%s", i, entry.WriterID)
	}

	// 3. Verify final digest matches head
	if !bytes.Equal(prevDigest, head.Digest) {
		return fmt.Errorf("chain verification failed: final digest mismatch (expected %x, got %x)",
			prevDigest, head.Digest)
	}

	// TODO: cache writer public keys to avoid repeated fetches
	// 4. Verify head signature using head writer's public key
	headWriterPubKey, err := kr.getWriterPublicKey(ctx, head.WriterID)
	if err != nil {
		return fmt.Errorf("failed to get head writer public key for %s: %w", head.WriterID, err)
	}
	if err := VerifyHeadSignature(head, headWriterPubKey); err != nil {
		return fmt.Errorf("head signature verification failed: %w", err)
	}

	// 5. Update local state
	localState.mu.Lock()
	localState.verifiedIdx = head.Idx
	localState.verifiedDigest = head.Digest
	localState.mu.Unlock()

	// 6. Seal the verified state for persistence across restarts
	if kr.PodId != "" {
		if err := sealVerifiedState(kr.PodId, head.Idx, head.Digest); err != nil {
			logDev("Warning: failed to seal verified state: %v", err)
		}
	}

	logDev("Chain verification complete: verified up to idx=%d", head.Idx)
	return nil
}

// executeHashChainTransaction atomically writes data, entry, and head to etcd
func (kr *KeyRegistry) executeHashChainTransaction(
	ctx context.Context,
	dataKey string,
	dataRecord *HashChainDataRecord,
	entry *HashChainEntry,
	newHead *HashChainHead,
	oldHead *HashChainHead,
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

	oldHeadBytes, err := json.Marshal(oldHead)
	if err != nil {
		return false, fmt.Errorf("failed to marshal old head record: %w", err)
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
			clientv3.Compare(clientv3.Value(HashChainHeadKey), "=", string(oldHeadBytes)), // Old head value unchanged
			clientv3.Compare(clientv3.ModRevision(HashChainHeadKey), "=", headModRev),     // Head unchanged
			clientv3.Compare(clientv3.CreateRevision(dataKey), "=", 0),                    // Write-once: data key doesn't exist
			clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),                   // Append-only: entry doesn't exist
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

// StoreEnclavePublicKeyWithHashChain stores an enclave public key with hash chain integrity.
// The public key and attestation report are packed together as an AttestedPublicKey.
func (kr *KeyRegistry) StoreEnclavePublicKeyWithHashChain(
	ctx context.Context,
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	attestationReport []byte,
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
		// First write: use genesis hash (no verification needed - nothing to verify yet)
		if len(genesisHash) == 0 {
			logDev("Warning: No genesis hash provided for first write, using zero hash")
			prevDigest = make([]byte, 32)
		} else {
			prevDigest = genesisHash
		}
		idx = 1
		isFirstWrite = true
		logDev("First write: using genesis hash as prev_digest")
	} else {
		// Advance local head to current head by verifying the full chain
		if err := kr.advanceAndVerifyChain(ctx, genesisHash, head); err != nil {
			return fmt.Errorf("chain verification failed (tampering detected): %w", err)
		}
		prevDigest = head.Digest
		idx = head.Idx + 1
		logDev("Chain verified, chaining from idx=%d", head.Idx)
	}

	// Pack public key and attestation report together
	attestedKey := &AttestedPublicKey{
		PublicKey:   enclavePubKey,
		Attestation: attestationReport,
	}
	payload, err := json.Marshal(attestedKey)
	if err != nil {
		return fmt.Errorf("failed to marshal attested public key: %w", err)
	}

	// Compute value hash (hash of the packed payload)
	payloadHash := sha256.Sum256(payload)

	// Create and sign data record
	dataSignMsg := computeDataSignatureMessage(dataKey, idx, payloadHash[:], writerID)
	dataSig := ed25519.Sign(enclavePrivKey, dataSignMsg)

	dataRecord := &HashChainDataRecord{
		Idx:      idx,
		WriterID: writerID,
		Payload:  payload,
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
	entrySignMsgHash := entrySignMsg
	newDigest := computeNewDigest(prevDigest, entrySignMsgHash, entry)

	// Create and sign new head
	headSignMsg := computeHeadSignatureMessage(idx, writerID, newDigest)
	headSig := ed25519.Sign(enclavePrivKey, headSignMsg)

	newHead := &HashChainHead{
		Idx:      idx,
		Digest:   newDigest, // how is newDigest computed?
		WriterID: writerID,
		HeadSig:  headSig,
	}

	// Execute atomic transaction
	oldHead := head
	success, err := kr.executeHashChainTransaction(ctx, dataKey, dataRecord, entry, newHead, oldHead, headModRev, isFirstWrite)
	if err != nil {
		return err
	}

	if !success {
		return fmt.Errorf("transaction conflict: head or data key changed")
	}

	logDev("Successfully stored enclave public key with hash chain: podID=%s, idx=%d", podID, idx)
	return nil
}

// StoreEnclavePublicKeyWithRetry stores an enclave public key with hash chain, retrying on conflicts.
// The public key and attestation report are packed together as an AttestedPublicKey.
func (kr *KeyRegistry) StoreEnclavePublicKeyWithRetry(
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	attestationReport []byte,
	genesisHash []byte,
	maxRetries int,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreEnclavePublicKeyWithRetry")

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := kr.StoreEnclavePublicKeyWithHashChain(ctx, podID, enclavePubKey, enclavePrivKey, attestationReport, genesisHash)
		cancel()

		if err == nil {
			logDev("Successfully stored enclave public key on attempt %d", attempt+1)
			return nil
		}

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

		// Other errors
		logDev("Error on attempt %d: %v", attempt+1, err)
		return err
	}

	return fmt.Errorf("failed to store enclave public key after %d attempts", maxRetries)
}

// TODO: remove this
// GetEnclavePublicKeyFromHashChain retrieves and verifies an enclave public key and its attestation from the hash chain.
// Returns the public key and attestation report packed as AttestedPublicKey.
func (kr *KeyRegistry) GetEnclavePublicKeyFromHashChain(
	ctx context.Context,
	podID string,
	clientPubKey ed25519.PublicKey,
) (*AttestedPublicKey, error) {
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

	// Verify data signature using the packed payload hash
	payloadHash := sha256.Sum256(dataRecord.Payload)
	dataSignMsg := computeDataSignatureMessage(dataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)

	// Unpack the AttestedPublicKey from the payload
	var attestedKey AttestedPublicKey
	if err := json.Unmarshal(dataRecord.Payload, &attestedKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal attested public key: %w", err)
	}

	// Validate public key size
	if len(attestedKey.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(attestedKey.PublicKey))
	}

	// Verify signature using the enclave's public key
	enclavePubKey := ed25519.PublicKey(attestedKey.PublicKey)
	if !ed25519.Verify(enclavePubKey, dataSignMsg, dataRecord.DataSig) {
		return nil, fmt.Errorf("data signature verification failed (tampering detected)")
	}

	logDev("Successfully verified enclave public key for pod %s at idx %d (attestation: %d bytes)", podID, dataRecord.Idx, len(attestedKey.Attestation))
	return &attestedKey, nil
}
