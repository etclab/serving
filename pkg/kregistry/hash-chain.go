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
	"math/rand"
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
	FlowMessagesPrefix     = "messages/"
)

// Flow tracking header constants
const (
	// FlowTrackingEnabledHeader is the internal header flag that flow tracking is enabled
	FlowTrackingEnabledHeader = "X-Flow-Tracking-Enabled"
)

// FlowRecord represents a flow processing record stored in the hash chain.
// Each service records its processing of a message (identified by flowID/nonce).
type FlowRecord struct {
	FlowID      string `json:"flow_id"`
	ServiceName string `json:"service_name"`
	PodID       string `json:"pod_id"`
	Timestamp   int64  `json:"timestamp"`
}

// FlowRecordResult holds the result of an async flow recording operation.
type FlowRecordResult struct {
	ChainIdx uint64
	Err      error
}

// flowResultContextKey is the context key for storing the flow result channel.
type flowResultContextKey struct{}

// FlowResultChan is a channel that receives the result of async flow recording.
type FlowResultChan <-chan FlowRecordResult

// formatEntryKey returns the etcd key for a hash chain entry at the given index.
// Uses zero-padded 20-digit format to ensure lexicographic order matches numeric order.
// This is critical because etcd range queries use lexicographic ordering.
func formatEntryKey(idx uint64) string {
	return fmt.Sprintf("%s%020d", HashChainEntryPrefix, idx)
}

// VerifiedPublicKeyCache caches writer public keys that have been verified
// (attestation + data signature). This avoids re-fetching and re-verifying
// the same public key multiple times during chain verification.
type VerifiedPublicKeyCache struct {
	mu   sync.RWMutex
	keys map[string]ed25519.PublicKey // writerID -> verified public key
}

// Package-level cache for verified writer public keys
var verifiedPubKeyCache = &VerifiedPublicKeyCache{
	keys: make(map[string]ed25519.PublicKey),
}

// Get returns a cached public key for the given writerID, or nil if not cached
func (c *VerifiedPublicKeyCache) Get(writerID string) ed25519.PublicKey {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.keys[writerID]
}

// Set caches a verified public key for the given writerID
func (c *VerifiedPublicKeyCache) Set(writerID string, pubKey ed25519.PublicKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys[writerID] = pubKey
}

// Constants for sealed state storage
const (
	SealedStateDir = "/sealed-state"
)

// SealedVerifiedState is the struct serialized for sealed storage
type SealedVerifiedState struct {
	VerifiedIdx    uint64 `json:"verified_idx"`
	VerifiedDigest []byte `json:"verified_digest"`
}

// SealedEnclaveKeypair is the struct serialized for sealed keypair storage.
// This allows pods to persist their enclave keypair across restarts so they
// can verify their own old entries in the hash chain.
type SealedEnclaveKeypair struct {
	PublicKey         []byte `json:"public_key"`
	PrivateKey        []byte `json:"private_key"`
	AttestationReport []byte `json:"attestation_report"`
}

// SealEnclaveKeypair persists the enclave keypair to disk using EGO sealing.
// The keypair is sealed with the enclave's product key, allowing it to survive
// enclave restarts as long as the signing key remains the same.
// Filename format: <podId>_keypair.sealed
func SealEnclaveKeypair(podID string, pubKey ed25519.PublicKey, privKey ed25519.PrivateKey, attestationReport []byte) error {
	logDev := mutil.LogWithPrefix("dev - SealEnclaveKeypair")

	keypair := SealedEnclaveKeypair{
		PublicKey:         pubKey,
		PrivateKey:        privKey,
		AttestationReport: attestationReport,
	}

	plaintext, err := json.Marshal(keypair)
	if err != nil {
		return fmt.Errorf("failed to marshal keypair: %w", err)
	}

	// Use podID as additional data to bind the sealed data to this pod
	additionalData := []byte(podID)

	sealed, err := ecrypto.SealWithProductKey(plaintext, additionalData)
	if err != nil {
		return fmt.Errorf("failed to seal keypair: %w", err)
	}

	// Ensure the sealed state directory exists
	if err := os.MkdirAll(SealedStateDir, 0700); err != nil {
		return fmt.Errorf("failed to create sealed state directory: %w", err)
	}

	// Filename format: <podId>_keypair.sealed
	fileName := fmt.Sprintf("%s_keypair.sealed", podID)
	filePath := filepath.Join(SealedStateDir, fileName)
	if err := os.WriteFile(filePath, sealed, 0600); err != nil {
		return fmt.Errorf("failed to write sealed keypair file: %w", err)
	}

	logDev("Sealed enclave keypair: podID=%s, file=%s", podID, filePath)
	return nil
}

// UnsealEnclaveKeypair loads the enclave keypair from sealed storage.
// Returns (nil, nil, nil, nil) if no sealed keypair exists (fresh start).
func UnsealEnclaveKeypair(podID string) (ed25519.PublicKey, ed25519.PrivateKey, []byte, error) {
	logDev := mutil.LogWithPrefix("dev - UnsealEnclaveKeypair")

	// Filename format: <podId>_keypair.sealed
	fileName := fmt.Sprintf("%s_keypair.sealed", podID)
	filePath := filepath.Join(SealedStateDir, fileName)

	sealed, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			logDev("No sealed keypair found for podID=%s (fresh start)", podID)
			return nil, nil, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("failed to read sealed keypair file: %w", err)
	}

	// Use podID as additional data (must match what was used during sealing)
	additionalData := []byte(podID)

	plaintext, err := ecrypto.Unseal(sealed, additionalData)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to unseal keypair: %w", err)
	}

	var keypair SealedEnclaveKeypair
	if err := json.Unmarshal(plaintext, &keypair); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to unmarshal keypair: %w", err)
	}

	if len(keypair.PublicKey) != ed25519.PublicKeySize {
		return nil, nil, nil, fmt.Errorf("invalid public key size: expected %d, got %d", ed25519.PublicKeySize, len(keypair.PublicKey))
	}
	if len(keypair.PrivateKey) != ed25519.PrivateKeySize {
		return nil, nil, nil, fmt.Errorf("invalid private key size: expected %d, got %d", ed25519.PrivateKeySize, len(keypair.PrivateKey))
	}

	logDev("Unsealed enclave keypair: podID=%s", podID)
	return ed25519.PublicKey(keypair.PublicKey), ed25519.PrivateKey(keypair.PrivateKey), keypair.AttestationReport, nil
}

// sealVerifiedState persists the verified state to disk using EGO sealing.
// The state is sealed with the enclave's product key, allowing it to survive
// enclave restarts as long as the signing key remains the same.
// Filename format: <podId>_<revisionId>_<idx>.sealed
func sealVerifiedState(podID, revisionID string, idx uint64, digest []byte) error {
	logDev := mutil.LogWithPrefix("dev - sealVerifiedState")

	state := SealedVerifiedState{
		VerifiedIdx:    idx,
		VerifiedDigest: digest,
	}

	plaintext, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal verified state: %w", err)
	}

	// Use podID+revisionID as additional data to bind the sealed data
	additionalData := []byte(podID + "_" + revisionID)

	sealed, err := ecrypto.SealWithProductKey(plaintext, additionalData)
	if err != nil {
		return fmt.Errorf("failed to seal verified state: %w", err)
	}

	// Ensure the sealed state directory exists
	if err := os.MkdirAll(SealedStateDir, 0700); err != nil {
		return fmt.Errorf("failed to create sealed state directory: %w", err)
	}

	// Filename format: <podId>_<revisionId>_<idx>.sealed
	fileName := fmt.Sprintf("%s_%s_%d.sealed", podID, revisionID, idx)
	filePath := filepath.Join(SealedStateDir, fileName)
	if err := os.WriteFile(filePath, sealed, 0600); err != nil {
		return fmt.Errorf("failed to write sealed state file: %w", err)
	}

	logDev("Sealed verified state: idx=%d, file=%s", idx, filePath)
	return nil
}

// unsealVerifiedState loads the verified state from sealed storage.
// It finds the sealed file with the highest index for the given revision.
// Returns (0, nil, nil) if no sealed state exists (fresh start).
// Note: podID is kept for API symmetry with sealVerifiedState but not used,
// as we search for sealed state from ANY pod for the given revision.
func unsealVerifiedState(_ /* podID */, revisionID string) (uint64, []byte, error) {
	logDev := mutil.LogWithPrefix("dev - unsealVerifiedState")

	// Find all sealed files for this revision (any pod)
	// Filename format: <podId>_<revisionId>_<idx>.sealed
	pattern := filepath.Join(SealedStateDir, fmt.Sprintf("*_%s_*.sealed", revisionID))
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to glob sealed state files: %w", err)
	}

	if len(matches) == 0 {
		logDev("No sealed state files found for revision %s (fresh start)", revisionID)
		return 0, nil, nil
	}

	// Find the file with the highest index
	var highestIdx uint64
	var highestFile string
	var highestPodID string

	for _, match := range matches {
		// Parse filename: <podId>_<revisionId>_<idx>.sealed
		baseName := filepath.Base(match)
		baseName = strings.TrimSuffix(baseName, ".sealed")

		// The suffix is _<revisionId>_<idx>, extract the index (last part after _)
		// We know the revisionId, so we can find where it ends
		suffix := "_" + revisionID + "_"
		suffixIdx := strings.Index(baseName, suffix)
		if suffixIdx == -1 {
			continue
		}

		filePodID := baseName[:suffixIdx]
		idxStr := baseName[suffixIdx+len(suffix):]

		idx, err := strconv.ParseUint(idxStr, 10, 64)
		if err != nil {
			logDev("Warning: failed to parse idx from filename %s: %v", baseName, err)
			continue
		}

		if idx > highestIdx {
			highestIdx = idx
			highestFile = match
			highestPodID = filePodID
		}
	}

	if highestFile == "" {
		logDev("No valid sealed state files found for revision %s", revisionID)
		return 0, nil, nil
	}

	logDev("Found sealed state file with highest idx=%d: %s", highestIdx, highestFile)

	sealed, err := os.ReadFile(highestFile)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to read sealed state file: %w", err)
	}

	// Use podID+revisionID as additional data (must match what was used during sealing)
	additionalData := []byte(highestPodID + "_" + revisionID)

	plaintext, err := ecrypto.Unseal(sealed, additionalData)
	if err != nil {
		return 0, nil, fmt.Errorf("failed to unseal verified state: %w", err)
	}

	var state SealedVerifiedState
	if err := json.Unmarshal(plaintext, &state); err != nil {
		return 0, nil, fmt.Errorf("failed to unmarshal verified state: %w", err)
	}

	logDev("Unsealed verified state: idx=%d, digest=%x (from pod %s)", state.VerifiedIdx, state.VerifiedDigest, highestPodID)
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
// Verified public keys are cached to avoid re-fetching and re-verifying.
//
// Trust chain:
// 1. Verify attestation report (Intel's root of trust)
// 2. Verify SHA256(writerID || publicKey) matches report.Data
// 3. Trust the public key
// 4. Verify data signature for integrity
// 5. Use trusted public key for hash chain signature verification
func (kr *KeyRegistry) getWriterPublicKey(ctx context.Context, writerID string) (ed25519.PublicKey, error) {
	logDev := mutil.LogWithPrefix("dev - getWriterPublicKey")

	// If writerID is our own pod, return our local public key directly
	if writerID == kr.PodId && kr.EnclavePublicKey != nil {
		logDev("Using local public key for self (writerID %s)", writerID)
		return kr.EnclavePublicKey, nil
	}

	// Check cache first
	if cachedKey := verifiedPubKeyCache.Get(writerID); cachedKey != nil {
		logDev("Using cached public key for writerID %s", writerID)
		return cachedKey, nil
	}

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

	// 5. Cache and return the trusted public key
	verifiedPubKeyCache.Set(writerID, pubKey)
	logDev("Cached verified public key for writerID %s", writerID)

	return pubKey, nil
}

// ============================================================
// Generic Hash Chain Storage Functions
// ============================================================

// StoreWithHashChain stores arbitrary data in the hash chain with tamper-evident integrity.
// This is a generic function that can store any payload type in the hash chain.
//
// Parameters:
//   - ctx: context for cancellation
//   - dataKey: the etcd key to store the data at
//   - payload: any Go type (will be JSON marshaled internally)
//
// The function uses kr.PodId as the writerID and kr.EnclavePrivateKey for signing.
// The function trusts the watcher's verified state. For first write (when watcher has no state),
// it uses a zero hash as prevDigest, consistent with the watcher's fallback behavior.
func (kr *KeyRegistry) StoreWithHashChain(
	ctx context.Context,
	dataKey string,
	payload interface{},
) error {
	logDev := mutil.LogWithPrefix("dev - StoreWithHashChain")

	if kr.EnclavePrivateKey == nil {
		return fmt.Errorf("enclave private key is nil")
	}

	writerID := kr.PodId
	signingKey := kr.EnclavePrivateKey

	// JSON marshal the payload
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	// Get watcher's verified state - includes genesis hash for first write
	watcherIdx, watcherDigest, genesisHash, headModRev, oldHead := GetWatcherVerifiedState()

	idx := watcherIdx + 1
	prevDigest := watcherDigest
	isFirstWrite := watcherIdx == 0

	if isFirstWrite {
		// Mirror the verifier's logic: use genesis hash if available, else zero hash
		if len(genesisHash) > 0 {
			prevDigest = genesisHash
		} else {
			prevDigest = make([]byte, 32)
		}
	}

	logDev("Chaining from idx=%d, isFirstWrite=%v, usingGenesis=%v", watcherIdx, isFirstWrite, len(genesisHash) > 0)

	// Compute value hash (hash of the payload)
	payloadHash := sha256.Sum256(payloadBytes)

	// Create and sign data record
	dataSignMsg := computeDataSignatureMessage(dataKey, idx, payloadHash[:], writerID)
	dataSig := ed25519.Sign(signingKey, dataSignMsg)

	dataRecord := &HashChainDataRecord{
		Idx:      idx,
		WriterID: writerID,
		Payload:  payloadBytes,
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
	entry.EntrySig = ed25519.Sign(signingKey, entrySignMsg)

	// Compute new digest
	entrySignMsgHash := entrySignMsg
	newDigest := computeNewDigest(prevDigest, entrySignMsgHash, entry)

	// Create and sign new head
	headSignMsg := computeHeadSignatureMessage(idx, writerID, newDigest)
	headSig := ed25519.Sign(signingKey, headSignMsg)

	newHead := &HashChainHead{
		Idx:      idx,
		Digest:   newDigest,
		WriterID: writerID,
		HeadSig:  headSig,
	}

	// Execute atomic transaction
	success, err := kr.executeHashChainTransaction(ctx, dataKey, dataRecord, entry, newHead, oldHead, headModRev, isFirstWrite)
	if err != nil {
		return err
	}

	if !success {
		return fmt.Errorf("transaction conflict: head or data key changed")
	}

	logDev("Successfully stored data with hash chain: dataKey=%s, idx=%d", dataKey, idx)
	return nil
}

// addJitter adds random jitter to a duration to prevent thundering herd.
// Returns duration + random(0, duration/2), i.e., up to 50% additional delay.
func addJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(d / 2)))
	return d + jitter
}

// StoreWithHashChainAndRetry stores data in the hash chain with automatic retry on conflicts.
// Implements exponential backoff with jitter (100ms initial, 5s max).
// Returns immediately on write-once violations (key already exists).
//
// Parameters:
//   - dataKey: the etcd key to store the data at
//   - payload: any Go type (will be JSON marshaled internally)
//   - maxRetries: maximum number of retry attempts
//
// The function uses kr.PodId as the writerID and kr.EnclavePrivateKey for signing.
func (kr *KeyRegistry) StoreWithHashChainAndRetry(
	dataKey string,
	payload interface{},
	maxRetries int,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreWithHashChainAndRetry")

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := kr.StoreWithHashChain(ctx, dataKey, payload)
		cancel()

		if err == nil {
			logDev("Successfully stored data on attempt %d", attempt+1)
			return nil
		}

		// Check if it's a conflict error (retryable)
		if strings.Contains(err.Error(), "transaction conflict") {
			sleepDuration := addJitter(backoff)
			logDev("Transaction conflict on attempt %d, retrying after %v (with jitter), dataKey: %s", attempt+1, sleepDuration, dataKey)
			time.Sleep(sleepDuration)
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

	return fmt.Errorf("failed to store data after %d attempts", maxRetries)
}

// ============================================================
// Hash Chain Transaction Execution
// ============================================================

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

	entryKey := formatEntryKey(entry.Idx)

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
		oldHeadBytes, err := json.Marshal(oldHead)
		if err != nil {
			return false, fmt.Errorf("failed to marshal old head record: %w", err)
		}

		logDev("Executing subsequent write transaction (updating head, modRev=%d)", headModRev)
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
// This function fully trusts the watcher's verified state - if the watcher hasn't started
// or has no state yet, it returns an error to trigger a retry.
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

	// Get watcher's verified state (ignore watcher's genesis hash, use parameter instead)
	watcherIdx, watcherDigest, _, headModRev, oldHead := GetWatcherVerifiedState()

	idx := watcherIdx + 1
	prevDigest := watcherDigest
	isFirstWrite := watcherIdx == 0

	if isFirstWrite {
		// Use genesis hash parameter if available, else zero hash
		if len(genesisHash) > 0 {
			prevDigest = genesisHash
		} else {
			logDev("Warning: No genesis hash provided for first write, using zero hash")
			prevDigest = make([]byte, 32)
		}
	}

	logDev("Chaining from idx=%d, isFirstWrite=%v", watcherIdx, isFirstWrite)

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

// UNUSED
// StoreEnclavePublicKeyWithRetry stores an enclave public key with hash chain, retrying on conflicts.
// Implements exponential backoff with jitter (100ms initial, 5s max).
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
			sleepDuration := addJitter(backoff)
			logDev("Transaction conflict on attempt %d, retrying after %v (with jitter), dataKey: %s", attempt+1, sleepDuration, "enclavePublicKey")
			time.Sleep(sleepDuration)
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

// ============================================================
// Hash Chain Verified Storage Functions
// ============================================================

// StoreEnclavePublicKeyWithHashChainVerified stores an enclave public key after
// verifying the entire hash chain from genesis to current head.
// This is used for the first write when no watcher is running yet.
//
// The function follows the same logic as catchUpHashChain:
// 1. Uses VerifyChainUpToHead to verify all entries from idx 1 up to current head
// 2. Updates watcher state with verified entries and data records
// 3. Queues data records for pending processing (since our public key isn't published yet)
// 4. Chains and writes our enclave public key
// 5. Updates watcher's verified state with our new entry
//
// After this function succeeds, the caller should:
// - Mark enclave public key as published
// - Call ProcessPendingKeyRecords to process deferred key records
func (kr *KeyRegistry) StoreEnclavePublicKeyWithHashChainVerified(
	ctx context.Context,
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	attestationReport []byte,
	genesisHash []byte,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreEnclavePublicKeyWithHashChainVerified")

	if enclavePubKey == nil || enclavePrivKey == nil {
		return fmt.Errorf("enclave keypair is nil")
	}

	dataKey := EnclaveKeysPrefix + podID + EnclavePublicKeySuffix
	writerID := podID

	// Use shared verification helper - verifies chain from genesis (same as catchUpHashChain)
	startDigest := genesisHash
	if len(startDigest) == 0 {
		logDev("Warning: No genesis hash provided, using zero hash")
		startDigest = make([]byte, 32)
	}

	result, err := kr.VerifyChainUpToHead(ctx, 1, startDigest)
	if err != nil {
		return fmt.Errorf("chain verification failed: %w", err)
	}

	// Update watcher state with verified entries and data records (same as catchUpHashChain)
	// This is important so that subsequent writes can chain correctly
	UpdateWatcherWithVerifiedEntries(result)

	// Queue data records that write to etcd for pending processing.
	// We can't process them now because our enclave public key isn't published yet.
	// Any writes they attempt would fail verification by other pods.
	// Re-encryption keys don't write to etcd, so they can be processed immediately.
	for dataKey, dataRecord := range result.DataRecords {
		if isReEncryptionKey(dataKey) {
			// Re-encryption keys don't write to etcd, process immediately
			logDev("Processing re-encryption key immediately: %s", dataKey)
			go kr.processVerifiedReEncryptionKeys(dataKey, dataRecord, dataRecord.WriterID)
		} else {
			// Determine if this key type needs deferral (writes to etcd)
			keyType := determineKeyTypeForDeferral(dataKey)
			if keyType != "" {
				logDev("Queueing %s key for pending processing: %s", keyType, dataKey)
				addPendingKeyRecord(dataKey, dataRecord, dataRecord.WriterID, keyType)
			}
		}
	}

	var idx uint64
	var prevDigest []byte
	var isFirstWrite bool
	var oldHead *HashChainHead
	var headModRev int64

	if result.Head == nil {
		// No head exists - this is the first entry in the chain
		isFirstWrite = true
		idx = 1
		prevDigest = startDigest
		oldHead = nil
		headModRev = 0
		logDev("No existing chain head, will create first entry with idx=1")
	} else {
		// Chain exists and is verified
		isFirstWrite = false
		idx = result.VerifiedIdx + 1
		prevDigest = result.VerifiedDigest
		oldHead = result.Head
		headModRev = result.HeadModRev
		logDev("Verified chain up to idx=%d, will write at idx=%d", result.VerifiedIdx, idx)
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
		Digest:   newDigest,
		WriterID: writerID,
		HeadSig:  headSig,
	}

	// Execute atomic transaction
	success, err := kr.executeHashChainTransaction(ctx, dataKey, dataRecord, entry, newHead, oldHead, headModRev, isFirstWrite)
	if err != nil {
		return err
	}

	if !success {
		return fmt.Errorf("transaction conflict: head or data key changed")
	}

	// Update watcher's verified state so subsequent writes can chain correctly
	// This is critical - after our write, the watcher should know about the new state
	// so other writes don't re-verify the entire chain
	UpdateVerifiedState(idx, newDigest, genesisHash, newHead, headModRev+1)

	logDev("Successfully stored enclave public key with verified chain: podID=%s, idx=%d", podID, idx)
	return nil
}

// StoreEnclavePublicKeyWithRetryVerified stores an enclave public key with hash chain,
// verifying the chain before each write attempt and retrying on conflicts.
// This is the preferred method for initial enclave public key publishing because it
// doesn't require the watcher to be running first.
func (kr *KeyRegistry) StoreEnclavePublicKeyWithRetryVerified(
	podID string,
	enclavePubKey ed25519.PublicKey,
	enclavePrivKey ed25519.PrivateKey,
	attestationReport []byte,
	genesisHash []byte,
	maxRetries int,
) error {
	logDev := mutil.LogWithPrefix("dev - StoreEnclavePublicKeyWithRetryVerified")

	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Longer timeout for verification
		err := kr.StoreEnclavePublicKeyWithHashChainVerified(ctx, podID, enclavePubKey, enclavePrivKey, attestationReport, genesisHash)
		cancel()

		if err == nil {
			logDev("Successfully stored enclave public key with verified chain on attempt %d", attempt+1)
			return nil
		}

		// Check if it's a conflict error (retryable)
		if strings.Contains(err.Error(), "transaction conflict") {
			sleepDuration := addJitter(backoff)
			logDev("Transaction conflict on attempt %d, retrying after %v (with jitter), dataKey: %s", attempt+1, sleepDuration, "enclavePublicKey")
			time.Sleep(sleepDuration)
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

	return fmt.Errorf("failed to store enclave public key with verified chain after %d attempts", maxRetries)
}

// ============================================================
// Flow Tracking Functions
// ============================================================

// formatFlowDataKey returns the etcd key for a flow record.
// Key format: messages/{flow_id}/{service_name}
// Example: messages/1738627200/validate-fun
func formatFlowDataKey(flowID, serviceName string) string {
	return fmt.Sprintf("%s%s/%s", FlowMessagesPrefix, flowID, serviceName)
}

// parseFlowDataKey extracts flowID and serviceName from a flow data key.
// Returns empty strings if the key doesn't match the expected format.
func parseFlowDataKey(dataKey string) (flowID, serviceName string) {
	if !strings.HasPrefix(dataKey, FlowMessagesPrefix) {
		return "", ""
	}

	// Remove "messages/" prefix
	rest := strings.TrimPrefix(dataKey, FlowMessagesPrefix)

	// Split: flow_id/service_name
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}

	return parts[0], parts[1]
}

// isFlowDataKey checks if a data key is a flow record key.
func isFlowDataKey(dataKey string) bool {
	return strings.HasPrefix(dataKey, FlowMessagesPrefix)
}

// RecordFlowProcessing stores a flow processing record in the hash chain.
// This records that this service (kr.ServiceName) has processed the given flowID.
// Returns the chain index where the record was stored, or an error.
//
// This is called during RoundTrip to record each message processed by this service,
// enabling replay detection (same flow processed twice by same service = replay).
func (kr *KeyRegistry) RecordFlowProcessing(flowID string) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - RecordFlowProcessing")

	if flowID == "" {
		return 0, fmt.Errorf("flowID is empty")
	}
	if kr.ServiceName == "" {
		return 0, fmt.Errorf("service name is not set")
	}

	dataKey := formatFlowDataKey(flowID, kr.ServiceName)

	flowRecord := &FlowRecord{
		FlowID:      flowID,
		ServiceName: kr.ServiceName,
		PodID:       kr.PodId,
		Timestamp:   time.Now().Unix(),
	}

	logDev("Recording flow processing: flowID=%s, serviceName=%s, dataKey=%s", flowID, kr.ServiceName, dataKey)

	// Store with retry using the existing hash chain storage mechanism
	// Use 10 retries to handle contention from concurrent writers
	err := kr.StoreWithHashChainAndRetry(dataKey, flowRecord, 10)
	if err != nil {
		// Check if it's already exists - this means replay detected at storage level
		if strings.Contains(err.Error(), "already exists") {
			return 0, fmt.Errorf("replay detected at storage: flow %s already recorded by %s", flowID, kr.ServiceName)
		}
		return 0, fmt.Errorf("failed to record flow processing: %w", err)
	}

	// Get the chain index from watcher state (it's the latest verified index)
	chainIdx, _, _, _, _ := GetWatcherVerifiedState()

	logDev("Successfully recorded flow processing: flowID=%s, chainIdx=%d", flowID, chainIdx)
	return chainIdx, nil
}

// StartFlowRecordingAsync starts recording flow processing in a background goroutine.
// Returns a context with the result channel embedded, and the channel itself.
// The caller should use WaitForFlowResult to get the result in EncryptResponseBody.
func (kr *KeyRegistry) StartFlowRecordingAsync(ctx context.Context, flowID string) (context.Context, FlowResultChan) {
	logDev := mutil.LogWithPrefix("dev - StartFlowRecordingAsync")

	resultChan := make(chan FlowRecordResult, 1)

	// Store the channel in context for EncryptResponseBody to retrieve
	newCtx := context.WithValue(ctx, flowResultContextKey{}, resultChan)

	go func() {
		defer close(resultChan)

		logDev("Starting async flow recording: flowID=%s", flowID)
		chainIdx, err := kr.RecordFlowProcessing(flowID)

		result := FlowRecordResult{
			ChainIdx: chainIdx,
			Err:      err,
		}

		logDev("Async flow recording complete: flowID=%s, chainIdx=%d, err=%v", flowID, chainIdx, err)
		resultChan <- result
	}()

	return newCtx, resultChan
}

// GetFlowResultChanFromContext retrieves the flow result channel from the context.
// Returns nil if no channel is present (flow tracking not enabled for this request).
func GetFlowResultChanFromContext(ctx context.Context) FlowResultChan {
	if ctx == nil {
		return nil
	}
	ch, ok := ctx.Value(flowResultContextKey{}).(chan FlowRecordResult)
	if !ok {
		return nil
	}
	return ch
}

// WaitForFlowResult waits for the async flow recording to complete and returns the result.
// If the context has no flow result channel, returns (0, nil) indicating flow tracking wasn't enabled.
// Uses a timeout to avoid blocking forever.
func WaitForFlowResult(ctx context.Context, timeout time.Duration) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - WaitForFlowResult")

	ch := GetFlowResultChanFromContext(ctx)
	if ch == nil {
		logDev("No flow result channel in context, flow tracking not enabled")
		return 0, nil
	}

	select {
	case result, ok := <-ch:
		if !ok {
			logDev("Flow result channel closed without result")
			return 0, fmt.Errorf("flow result channel closed unexpectedly")
		}
		logDev("Got flow result: chainIdx=%d, err=%v", result.ChainIdx, result.Err)
		return result.ChainIdx, result.Err
	case <-time.After(timeout):
		logDev("Timeout waiting for flow result after %v", timeout)
		return 0, fmt.Errorf("timeout waiting for flow recording to complete")
	case <-ctx.Done():
		logDev("Context cancelled while waiting for flow result")
		return 0, ctx.Err()
	}
}
