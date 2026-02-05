package kregistry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/etclab/pre"
	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"
)

// ============================================================
// Watch-Based Hash Chain Verification
// ============================================================

// PendingKeyRecord stores a key record that is waiting to be processed.
// This is used when key records are received before the enclave public key
// has been published, so processing must be deferred.
// Only records that write to etcd need deferral: leader (publicParams) and member keys.
type PendingKeyRecord struct {
	DataKey    string
	DataRecord *HashChainDataRecord
	WriterID   string
	KeyType    string // "leader" or "member" (only types that write to etcd)
}

// HashChainWatcher tracks the state of the hash chain verification watcher.
// It continuously verifies the hash chain as events arrive from etcd.
// It also stores verified heads, entries, and data records for use by writers.
type HashChainWatcher struct {
	mu             sync.RWMutex
	verifiedIdx    uint64 // Last verified entry index
	verifiedDigest []byte // Digest after last verified entry
	headModRev     int64  // ModRevision of the head when last verified
	genesisHash    []byte // Genesis hash for chain start
	started        bool   // Whether watcher is running

	// Verified chain data - stored for transaction validation
	verifiedHeads       map[uint64]*HashChainHead       // Verified heads by idx
	verifiedEntries     map[uint64]*HashChainEntry      // Verified entries by idx
	verifiedDataRecords map[string]*HashChainDataRecord // Verified data records by dataKey

	// Flow tracking - maps flowID -> serviceName -> chainIndex
	// Used for replay detection: if (flowID, serviceName) exists, that service already processed that flow
	verifiedFlows map[string]map[string]uint64
	muFlows       sync.RWMutex

	// Pending records for deferred processing (separate mutex from verified state)
	pendingMu      sync.Mutex
	pendingRecords []PendingKeyRecord // Key records awaiting processing
}

// enclavePublicKeyPublished tracks whether this pod's enclave public key has been
// successfully published to the hash chain. Processing of member keys that require
// writing back to etcd (like re-encryption key generation) must wait until this is true.
var enclavePublicKeyPublished atomic.Bool

// Package-level watcher state
var chainWatcher = &HashChainWatcher{
	verifiedHeads:       make(map[uint64]*HashChainHead),
	verifiedEntries:     make(map[uint64]*HashChainEntry),
	verifiedDataRecords: make(map[string]*HashChainDataRecord),
	verifiedFlows:       make(map[string]map[string]uint64),
}

// GetVerifiedState returns the current verified state from the watcher.
// Returns (idx, digest, genesisHash, headModRev, verifiedHead) for use by writers.
// The verifiedHead returned is the head at the current verifiedIdx.
// genesisHash is returned so writers can use it for first write (to match verifier logic).
func (w *HashChainWatcher) GetVerifiedState() (uint64, []byte, []byte, int64, *HashChainHead) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.verifiedIdx, w.verifiedDigest, w.genesisHash, w.headModRev, w.verifiedHeads[w.verifiedIdx]
}

// SetGenesisHash sets the genesis hash for the chain watcher without starting it.
// This must be called BEFORE any writes to the hash chain to ensure the first entry
// uses the correct genesis hash as its prev_digest.
func SetGenesisHash(genesisHash []byte) {
	chainWatcher.mu.Lock()
	defer chainWatcher.mu.Unlock()
	chainWatcher.genesisHash = genesisHash
}

// MarkEnclavePublicKeyPublished marks that this pod's enclave public key has been
// successfully published to the hash chain. This should be called after the
// enclave public key is successfully stored.
func MarkEnclavePublicKeyPublished() {
	enclavePublicKeyPublished.Store(true)
}

// IsEnclavePublicKeyPublished returns whether this pod's enclave public key
// has been successfully published to the hash chain.
func IsEnclavePublicKeyPublished() bool {
	return enclavePublicKeyPublished.Load()
}

// addPendingKeyRecord adds a key record to the pending queue for later processing.
// This is called when key records are received before the enclave public key is published.
// keyType should be "leader", "member", or "reencryption".
// Skips adding if a record with the same dataKey already exists (deduplication).
func addPendingKeyRecord(dataKey string, dataRecord *HashChainDataRecord, writerID string, keyType string) {
	chainWatcher.pendingMu.Lock()
	defer chainWatcher.pendingMu.Unlock()

	// Check if this dataKey is already in the pending queue
	for _, existing := range chainWatcher.pendingRecords {
		if existing.DataKey == dataKey {
			// Already queued, skip duplicate
			return
		}
	}

	chainWatcher.pendingRecords = append(chainWatcher.pendingRecords, PendingKeyRecord{
		DataKey:    dataKey,
		DataRecord: dataRecord,
		WriterID:   writerID,
		KeyType:    keyType,
	})
}

// ProcessPendingKeyRecords processes all pending key records that were queued
// while waiting for the enclave public key to be published.
// This should be called after MarkEnclavePublicKeyPublished().
// Uses processAllVerifiedKeys() for consistent routing logic.
// Deduplicates by dataKey to prevent multiple goroutines writing the same key.
func (kr *KeyRegistry) ProcessPendingKeyRecords() {
	logDev := mutil.LogWithPrefix("dev - ProcessPendingKeyRecords")

	chainWatcher.pendingMu.Lock()
	pending := chainWatcher.pendingRecords
	chainWatcher.pendingRecords = nil // Clear the queue
	chainWatcher.pendingMu.Unlock()

	if len(pending) == 0 {
		logDev("No pending key records to process")
		return
	}

	// Deduplicate by dataKey - keep only the latest record for each unique key
	// This prevents multiple goroutines from trying to write the same key
	deduped := make(map[string]PendingKeyRecord)
	for _, record := range pending {
		deduped[record.DataKey] = record
	}

	logDev("Processing %d pending key records (%d after dedup)", len(pending), len(deduped))
	for _, record := range deduped {
		// Use the same routing logic as normal chain verification
		kr.processAllVerifiedKeys(record.DataKey, record.DataRecord, record.WriterID)
	}
	logDev("Finished processing pending key records")
}

// ProcessPendingMemberKeys is kept for backwards compatibility.
// It now calls ProcessPendingKeyRecords which handles all key types.
func (kr *KeyRegistry) ProcessPendingMemberKeys() {
	kr.ProcessPendingKeyRecords()
}

// StartHashChainWatcher starts the hash chain verification watcher.
// It loads sealed state if available, catches up to the current head,
// and then watches for new entries and head updates.
func (kr *KeyRegistry) StartHashChainWatcher(genesisHash []byte) error {
	logDev := mutil.LogWithPrefix("dev - StartHashChainWatcher")

	chainWatcher.mu.Lock()
	if chainWatcher.started {
		chainWatcher.mu.Unlock()
		logDev("Hash chain watcher already started")
		return nil
	}
	chainWatcher.genesisHash = genesisHash
	chainWatcher.started = true
	chainWatcher.mu.Unlock()

	// Try to load sealed state for recovery
	if kr.PodId != "" && kr.FunctionId != "" {
		sealedIdx, sealedDigest, err := unsealVerifiedState(kr.PodId, kr.FunctionId)
		if err != nil {
			logDev("Warning: failed to unseal state: %v", err)
		} else if sealedIdx > 0 && sealedDigest != nil {
			chainWatcher.mu.Lock()
			chainWatcher.verifiedIdx = sealedIdx
			chainWatcher.verifiedDigest = sealedDigest
			chainWatcher.mu.Unlock()
			logDev("Loaded sealed state: idx=%d", sealedIdx)
		}
	}

	// Catch up to current state then start watching
	go kr.runHashChainWatcher()

	return nil
}

// runHashChainWatcher is the main watch loop that processes incoming entry events.
// We only watch for entries - the head is fetched and verified along with each entry
// since they are created together in the same transaction.
func (kr *KeyRegistry) runHashChainWatcher() {
	logDev := mutil.LogWithPrefix("dev - runHashChainWatcher")

	ctx := context.Background()

	// Catch up to current state first
	headModRev, err := kr.catchUpHashChain(ctx)
	if err != nil {
		logDev("Error catching up hash chain: %v", err)
		// Continue anyway - we'll try to verify what we can
	}

	// Watch entry prefix for new entries
	entryWatchCh := kr.Client().Watch(ctx, HashChainEntryPrefix,
		clientv3.WithPrefix(),
		clientv3.WithRev(headModRev+1))

	logDev("Started hash chain watcher from revision %d", headModRev+1)

	for wresp := range entryWatchCh {
		if wresp.Canceled {
			logDev("Entry watch canceled: %v", wresp.Err())
			return
		}
		for _, ev := range wresp.Events {
			if ev.Type == clientv3.EventTypePut {
				kr.handleEntryEvent(ctx, ev.Kv.Key, ev.Kv.Value, ev.Kv.ModRevision)
			}
		}
	}
}

// VerifyChainResult holds the result of chain verification
type VerifyChainResult struct {
	Head           *HashChainHead
	HeadModRev     int64
	VerifiedIdx    uint64
	VerifiedDigest []byte
	Entries        map[uint64]*HashChainEntry
	DataRecords    map[string]*HashChainDataRecord
}

// VerifyChainUpToHead verifies all entries from startIdx to the current head.
// This is a shared helper used by both catchUpHashChain and StoreEnclavePublicKeyWithHashChainVerified.
// It does NOT update watcher state or process keys - callers handle that.
//
// This implementation uses batch fetching and parallel verification for performance:
// - Phase 1: Batch-fetch all entries in a single range query
// - Phase 2: Pre-fetch all unique writer public keys (warms cache)
// - Phase 3: Batch-fetch all data records in a single transaction
// - Phase 4: Verify entries sequentially (chain dependency), but with parallel verification per entry
// - Phase 5: Verify head signature and digest match
//
// Parameters:
//   - startIdx: the first entry index to verify (1 for full verification from genesis)
//   - startDigest: the digest before startIdx (genesis hash if startIdx=1)
//
// Returns the verification result including all verified entries and data records.
func (kr *KeyRegistry) VerifyChainUpToHead(ctx context.Context, startIdx uint64, startDigest []byte) (*VerifyChainResult, error) {
	logDev := mutil.LogWithPrefix("dev - VerifyChainUpToHead")

	// Get current head
	head, headModRev, err := kr.GetHashChainHead(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get hash chain head: %w", err)
	}

	if head == nil {
		logDev("No hash chain head yet")
		return &VerifyChainResult{
			Head:           nil,
			HeadModRev:     0,
			VerifiedIdx:    0,
			VerifiedDigest: startDigest,
			Entries:        make(map[uint64]*HashChainEntry),
			DataRecords:    make(map[string]*HashChainDataRecord),
		}, nil
	}

	prevDigest := startDigest
	if len(prevDigest) == 0 {
		logDev("Warning: No start digest provided, using zero hash")
		prevDigest = make([]byte, 32)
	}

	logDev("Verifying chain from idx=%d to head.Idx=%d", startIdx, head.Idx)

	// Phase 1: Batch-fetch all entries
	entries, err := kr.batchFetchEntries(ctx, startIdx, head.Idx)
	if err != nil {
		return nil, fmt.Errorf("failed to batch fetch entries: %w", err)
	}

	if len(entries) == 0 {
		logDev("No entries to verify")
		return &VerifyChainResult{
			Head:           head,
			HeadModRev:     headModRev,
			VerifiedIdx:    startIdx - 1,
			VerifiedDigest: prevDigest,
			Entries:        make(map[uint64]*HashChainEntry),
			DataRecords:    make(map[string]*HashChainDataRecord),
		}, nil
	}

	// Collect data keys and unique writer IDs
	dataKeys := make([]string, len(entries))
	writerIDSet := make(map[string]struct{})
	for i, entry := range entries {
		dataKeys[i] = entry.DataKey
		writerIDSet[entry.WriterID] = struct{}{}
	}

	// Phase 2: Pre-fetch writer public keys (warms cache)
	uniqueWriterIDs := make([]string, 0, len(writerIDSet))
	for id := range writerIDSet {
		uniqueWriterIDs = append(uniqueWriterIDs, id)
	}
	if err := kr.prefetchWriterPublicKeys(ctx, uniqueWriterIDs); err != nil {
		return nil, err
	}

	// Phase 3: Batch-fetch all data records
	dataRecordsMap, err := kr.batchFetchDataRecords(ctx, dataKeys)
	if err != nil {
		return nil, fmt.Errorf("failed to batch fetch data records: %w", err)
	}

	// Phase 4: Verify entries sequentially (chain dependency), but with parallel verification per entry
	entriesResult := make(map[uint64]*HashChainEntry, len(entries))
	dataRecordsResult := make(map[string]*HashChainDataRecord, len(entries))

	for _, entry := range entries {
		dataRecord := dataRecordsMap[entry.DataKey]
		if dataRecord == nil {
			return nil, fmt.Errorf("data record not found for entry %d", entry.Idx)
		}

		// Get writer's public key (from cache, warmed in Phase 2)
		writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
		if err != nil {
			return nil, fmt.Errorf("failed to get writer public key for %s: %w", entry.WriterID, err)
		}

		// Parallel verification of entry + data
		if err := verifyEntryAndDataParallel(entry, dataRecord, writerPubKey, prevDigest); err != nil {
			return nil, err
		}

		// Compute new digest (must be sequential - needed for next entry's verification)
		entrySignHash := computeEntrySignatureMessage(entry)
		prevDigest = computeNewDigest(prevDigest, entrySignHash, entry)

		// Store in result maps
		entriesResult[entry.Idx] = entry
		dataRecordsResult[entry.DataKey] = dataRecord

		logDev("Verified entry idx=%d, dataKey=%s", entry.Idx, entry.DataKey)
	}

	// Phase 5: Verify head signature and digest match
	if err := kr.verifyHead(ctx, head, prevDigest); err != nil {
		return nil, err
	}

	logDev("Verified chain up to idx=%d", head.Idx)
	return &VerifyChainResult{
		Head:           head,
		HeadModRev:     headModRev,
		VerifiedIdx:    head.Idx,
		VerifiedDigest: prevDigest,
		Entries:        entriesResult,
		DataRecords:    dataRecordsResult,
	}, nil
}

// catchUpHashChain fetches and verifies existing entries up to current head.
// Returns the head's ModRevision for starting the watch.
func (kr *KeyRegistry) catchUpHashChain(ctx context.Context) (int64, error) {
	logDev := mutil.LogWithPrefix("dev - catchUpHashChain")

	// Determine where to start verification
	chainWatcher.mu.RLock()
	startIdx := chainWatcher.verifiedIdx + 1
	startDigest := chainWatcher.verifiedDigest
	chainWatcher.mu.RUnlock()

	// Initialize from genesis if needed
	if startIdx == 1 || startDigest == nil {
		startIdx = 1
		startDigest = chainWatcher.genesisHash
		if len(startDigest) == 0 {
			logDev("Warning: No genesis hash provided, using zero hash")
			startDigest = make([]byte, 32)
		}
	}

	// Use shared verification helper
	result, err := kr.VerifyChainUpToHead(ctx, startIdx, startDigest)
	if err != nil {
		return 0, err
	}

	if result.Head == nil {
		logDev("No hash chain head yet, starting fresh")
		return 0, nil
	}

	// Update watcher state with verified entries and data records
	UpdateWatcherWithVerifiedEntries(result)

	// Process leader, member, re-encryption keys, and flow records from catch-up entries
	// This is independent of chain validation and can run in separate goroutines
	for dataKey, dataRecord := range result.DataRecords {
		entry := result.Entries[dataRecord.Idx]
		if entry != nil {
			// Process flow records for replay detection
			processFlowRecordIfNeeded(dataKey, entry.Idx)
			// Process key records (non-blocking)
			kr.processAllVerifiedKeys(dataKey, dataRecord, entry.WriterID)
		}
	}

	// Seal state for persistence
	if kr.PodId != "" && kr.FunctionId != "" {
		if err := sealVerifiedState(kr.PodId, kr.FunctionId, result.VerifiedIdx, result.VerifiedDigest); err != nil {
			logDev("Warning: failed to seal verified state: %v", err)
		}
	}

	logDev("Caught up to idx=%d", result.VerifiedIdx)
	return result.HeadModRev, nil
}

// verifyAndProcessEntryWithData verifies a single hash chain entry and returns the data record.
// It checks the chain link, writer's attestation, entry signature, and data signature.
// Returns the data record for storage in the watcher cache.
func (kr *KeyRegistry) verifyAndProcessEntryWithData(ctx context.Context, entry *HashChainEntry, prevDigest []byte) (*HashChainDataRecord, error) {
	logDev := mutil.LogWithPrefix("dev - verifyAndProcessEntryWithData")

	// 1. Verify prev_digest chain link
	if !bytes.Equal(entry.PrevDigest, prevDigest) {
		return nil, fmt.Errorf("chain broken at idx %d: prev_digest mismatch (expected %x, got %x)",
			entry.Idx, prevDigest, entry.PrevDigest)
	}

	// 2. Get writer's public key (verifies attestation)
	writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get writer public key for %s: %w", entry.WriterID, err)
	}

	// 3. Verify entry signature
	if err := verifyEntrySignature(entry, writerPubKey); err != nil {
		return nil, fmt.Errorf("entry signature verification failed at idx %d: %w", entry.Idx, err)
	}

	// 4. Fetch and verify the data referenced by this entry
	dataRecord, err := kr.verifyEntryDataWithRecord(ctx, entry, writerPubKey)
	if err != nil {
		return nil, fmt.Errorf("data verification failed at idx %d: %w", entry.Idx, err)
	}

	logDev("Verified entry idx=%d, writer=%s", entry.Idx, entry.WriterID)
	return dataRecord, nil
}

// verifyEntryDataWithRecord fetches and verifies the data record referenced by an entry.
// Returns the verified data record for caching.
func (kr *KeyRegistry) verifyEntryDataWithRecord(ctx context.Context, entry *HashChainEntry, writerPubKey ed25519.PublicKey) (*HashChainDataRecord, error) {
	logDev := mutil.LogWithPrefix("dev - verifyEntryDataWithRecord")

	// Fetch the data record
	resp, err := kr.Client().Get(ctx, entry.DataKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get data at %s: %w", entry.DataKey, err)
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("data not found at %s", entry.DataKey)
	}

	var dataRecord HashChainDataRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &dataRecord); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data record: %w", err)
	}

	// Verify payload hash matches entry's value_hash
	payloadHash := sha256.Sum256(dataRecord.Payload)
	if !bytes.Equal(payloadHash[:], entry.ValueHash) {
		return nil, fmt.Errorf("payload hash mismatch at idx %d (expected %x, got %x)",
			entry.Idx, entry.ValueHash, payloadHash[:])
	}

	// Verify data signature
	dataSignMsg := computeDataSignatureMessage(entry.DataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)
	if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
		return nil, fmt.Errorf("data signature verification failed at %s", entry.DataKey)
	}

	logDev("Verified data at %s", entry.DataKey)
	return &dataRecord, nil
}

// verifyHead verifies the hash chain head signature and digest.
func (kr *KeyRegistry) verifyHead(ctx context.Context, head *HashChainHead, expectedDigest []byte) error {
	// Verify digest matches
	if !bytes.Equal(head.Digest, expectedDigest) {
		return fmt.Errorf("head digest mismatch (expected %x, got %x)", expectedDigest, head.Digest)
	}

	// Verify head signature
	headWriterPubKey, err := kr.getWriterPublicKey(ctx, head.WriterID)
	if err != nil {
		return fmt.Errorf("failed to get head writer public key: %w", err)
	}

	return VerifyHeadSignature(head, headWriterPubKey)
}

// verifyEntryAndDataParallel verifies an entry and its data record in parallel.
// This is the shared verification core used by both handleEntryEvent (watch path)
// and VerifyChainUpToHead (catch-up path).
//
// Parameters:
//   - entry: the hash chain entry to verify
//   - dataRecord: the data record (already fetched)
//   - writerPubKey: the verified public key of the writer
//   - expectedPrevDigest: the expected prev_digest (chain link verification)
//
// Returns nil if verification succeeds, error otherwise.
// NOTE: Does NOT verify head - caller handles that separately.
func verifyEntryAndDataParallel(
	entry *HashChainEntry,
	dataRecord *HashChainDataRecord,
	writerPubKey ed25519.PublicKey,
	expectedPrevDigest []byte,
) error {
	errCh := make(chan error, 2)

	// Goroutine 1: Verify entry (chain link + signature)
	go func() {
		if !bytes.Equal(entry.PrevDigest, expectedPrevDigest) {
			errCh <- fmt.Errorf("chain broken at idx %d: prev_digest mismatch (expected %x, got %x)",
				entry.Idx, expectedPrevDigest, entry.PrevDigest)
			return
		}
		if err := verifyEntrySignature(entry, writerPubKey); err != nil {
			errCh <- fmt.Errorf("entry signature verification failed at idx %d: %w", entry.Idx, err)
			return
		}
		errCh <- nil
	}()

	// Goroutine 2: Verify data record
	go func() {
		payloadHash := sha256.Sum256(dataRecord.Payload)
		if !bytes.Equal(payloadHash[:], entry.ValueHash) {
			errCh <- fmt.Errorf("payload hash mismatch at idx %d (expected %x, got %x)",
				entry.Idx, entry.ValueHash, payloadHash[:])
			return
		}
		dataSignMsg := computeDataSignatureMessage(entry.DataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)
		if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
			errCh <- fmt.Errorf("data signature verification failed at %s", entry.DataKey)
			return
		}
		errCh <- nil
	}()

	// Wait for both goroutines
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			// Drain remaining goroutine
			for j := i + 1; j < 2; j++ {
				<-errCh
			}
			return err
		}
	}
	return nil
}

// batchFetchEntries fetches multiple entries in a single etcd range query.
// Returns entries sorted by index.
// Uses zero-padded keys to ensure lexicographic order matches numeric order.
func (kr *KeyRegistry) batchFetchEntries(ctx context.Context, startIdx, endIdx uint64) ([]*HashChainEntry, error) {
	startKey := formatEntryKey(startIdx)
	endKey := formatEntryKey(endIdx + 1)

	resp, err := kr.Client().Get(ctx, startKey, clientv3.WithRange(endKey))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch entries range: %w", err)
	}

	entries := make([]*HashChainEntry, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var entry HashChainEntry
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry: %w", err)
		}
		entries = append(entries, &entry)
	}

	// Sort by index (etcd may not guarantee order with range)
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Idx < entries[j].Idx
	})

	return entries, nil
}

// batchFetchDataRecords fetches multiple data records in a single etcd transaction.
// Returns a map from dataKey to data record.
func (kr *KeyRegistry) batchFetchDataRecords(ctx context.Context, dataKeys []string) (map[string]*HashChainDataRecord, error) {
	if len(dataKeys) == 0 {
		return make(map[string]*HashChainDataRecord), nil
	}

	// Build transaction with all data key gets
	ops := make([]clientv3.Op, len(dataKeys))
	for i, key := range dataKeys {
		ops[i] = clientv3.OpGet(key)
	}

	txnResp, err := kr.Client().Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return nil, fmt.Errorf("batch fetch transaction failed: %w", err)
	}

	result := make(map[string]*HashChainDataRecord, len(dataKeys))
	for i, key := range dataKeys {
		rangeResp := txnResp.Responses[i].GetResponseRange()
		if len(rangeResp.Kvs) == 0 {
			return nil, fmt.Errorf("data not found at %s", key)
		}
		var record HashChainDataRecord
		if err := json.Unmarshal(rangeResp.Kvs[0].Value, &record); err != nil {
			return nil, fmt.Errorf("failed to unmarshal data record at %s: %w", key, err)
		}
		result[key] = &record
	}

	return result, nil
}

// prefetchWriterPublicKeys fetches and caches public keys for all unique writers.
// This reduces per-entry round trips during verification.
func (kr *KeyRegistry) prefetchWriterPublicKeys(ctx context.Context, writerIDs []string) error {
	for _, writerID := range writerIDs {
		// getWriterPublicKey already uses cache, so this just warms it
		_, err := kr.getWriterPublicKey(ctx, writerID)
		if err != nil {
			return fmt.Errorf("failed to fetch writer public key for %s: %w", writerID, err)
		}
	}
	return nil
}

// handleEntryEvent processes an incoming entry event from the watch.
// It verifies the entry signature, fetches/verifies the data record and head in a single transaction.
// The entry, data, and head are created together in a transaction, so they share the same WriterID.
// Verifications are done in parallel for performance using verifyEntryAndDataParallel for entry+data
// and a separate goroutine for head verification.
// If the entry was written by this pod, skip verification since we already verified during store.
func (kr *KeyRegistry) handleEntryEvent(ctx context.Context, key, value []byte, modRevision int64) {
	logDev := mutil.LogWithPrefix("dev - handleEntryEvent")

	var entry HashChainEntry
	if err := json.Unmarshal(value, &entry); err != nil {
		logDev("Error unmarshaling entry: %v", err)
		return
	}

	logDev("Received entry event for key: %s, modRevision: %d, dataKey: %s", string(key), modRevision, entry.DataKey)

	chainWatcher.mu.Lock()
	defer chainWatcher.mu.Unlock()

	prevDigest := chainWatcher.verifiedDigest
	if prevDigest == nil {
		prevDigest = chainWatcher.genesisHash
		if len(prevDigest) == 0 {
			prevDigest = make([]byte, 32)
		}
	}

	// Pre-compute newDigest (needed for head verification)
	entrySignHash := computeEntrySignatureMessage(&entry)
	newDigest := computeNewDigest(prevDigest, entrySignHash, &entry)

	// Get writer's public key once - entry, data, and head all share the same writerID
	// since they are created in the same transaction by the same writer
	writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
	if err != nil {
		logDev("Failed to get writer public key for %s: %v", entry.WriterID, err)
		return
	}

	// Fetch data and head in single transaction
	dataRecord, head, headModRev, err := kr.fetchDataAndHead(ctx, entry.DataKey, modRevision)
	if err != nil {
		logDev("Failed to fetch data and head: %v", err)
		return
	}

	// Run entry+data verification and head verification in parallel
	errCh := make(chan error, 2)

	// Goroutine 1: Entry + Data verification (using shared function)
	go func() {
		errCh <- verifyEntryAndDataParallel(&entry, dataRecord, writerPubKey, prevDigest)
	}()

	// Goroutine 2: Head verification
	go func() {
		if !bytes.Equal(head.Digest, newDigest) {
			errCh <- fmt.Errorf("head digest mismatch (expected %x, got %x)", newDigest, head.Digest)
			return
		}
		if err := VerifyHeadSignature(head, writerPubKey); err != nil {
			errCh <- fmt.Errorf("head signature verification failed: %w", err)
			return
		}
		errCh <- nil
	}()

	// Wait for both verifications
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			logDev("Verification failed: %v", err)
			for j := i + 1; j < 2; j++ {
				<-errCh
			}
			return
		}
	}

	// All verifications passed - update state and store verified entry/data/head
	chainWatcher.updateVerifiedStateLocked(entry.Idx, newDigest, headModRev, head)
	chainWatcher.storeVerifiedEntryLocked(&entry, dataRecord)

	// Check if this is a flow record and store for replay detection
	processFlowRecordIfNeeded(entry.DataKey, entry.Idx)

	// Seal state for persistence
	if kr.PodId != "" && kr.FunctionId != "" {
		if err := sealVerifiedState(kr.PodId, kr.FunctionId, entry.Idx, newDigest); err != nil {
			logDev("Warning: failed to seal verified state: %v", err)
		}
	}

	logDev("Verified entry idx=%d with head, modRev=%d, dataKey=%s", entry.Idx, headModRev, entry.DataKey)

	// Process verified keys (non-blocking)
	kr.processAllVerifiedKeys(entry.DataKey, dataRecord, entry.WriterID)
}

// fetchDataAndHead fetches both the data record and head in a single etcd transaction.
// The modRevision parameter ensures we fetch the head at the exact revision when the entry was written,
// since the head is a single key that gets updated with each new entry.
func (kr *KeyRegistry) fetchDataAndHead(ctx context.Context, dataKey string, modRevision int64) (*HashChainDataRecord, *HashChainHead, int64, error) {
	// Use a transaction to fetch both keys in a single round trip
	// Fetch the head at the specific revision when the entry was created
	// (entries and data are new keys, but head is an existing key that gets updated)
	txnResp, err := kr.Client().Txn(ctx).Then(
		clientv3.OpGet(dataKey),
		clientv3.OpGet(HashChainHeadKey, clientv3.WithRev(modRevision)),
	).Commit()
	if err != nil {
		return nil, nil, 0, fmt.Errorf("transaction failed: %w", err)
	}

	// Parse data record response
	dataResp := txnResp.Responses[0].GetResponseRange()
	if len(dataResp.Kvs) == 0 {
		return nil, nil, 0, fmt.Errorf("data not found at %s", dataKey)
	}
	var dataRecord HashChainDataRecord
	if err := json.Unmarshal(dataResp.Kvs[0].Value, &dataRecord); err != nil {
		return nil, nil, 0, fmt.Errorf("failed to unmarshal data record: %w", err)
	}

	// Parse head response
	headResp := txnResp.Responses[1].GetResponseRange()
	if len(headResp.Kvs) == 0 {
		return nil, nil, 0, fmt.Errorf("head not found")
	}
	var head HashChainHead
	if err := json.Unmarshal(headResp.Kvs[0].Value, &head); err != nil {
		return nil, nil, 0, fmt.Errorf("failed to unmarshal head: %w", err)
	}

	return &dataRecord, &head, headResp.Kvs[0].ModRevision, nil
}

// IsChainVerified checks if the watcher has verified the chain up to a given index.
func IsChainVerified(idx uint64) bool {
	chainWatcher.mu.RLock()
	defer chainWatcher.mu.RUnlock()
	return chainWatcher.verifiedIdx >= idx
}

// GetWatcherVerifiedState returns the current verified state from the watcher.
// Returns (idx, digest, genesisHash, headModRev, verifiedHead) or (0, nil, nil, 0, nil) if no state is verified yet.
// genesisHash is returned so writers can use it for first write (to match verifier logic).
func GetWatcherVerifiedState() (uint64, []byte, []byte, int64, *HashChainHead) {
	return chainWatcher.GetVerifiedState()
}

// UpdateVerifiedState updates the watcher's verified state after a successful write.
// This is called after StoreEnclavePublicKeyWithHashChainVerified to sync the watcher state
// so subsequent writes can chain correctly without re-verifying the entire chain.
//
// Parameters:
//   - idx: the new verified index (the index just written)
//   - digest: the new chain digest after the write
//   - genesisHash: the genesis hash (in case it wasn't set before)
//   - head: the new head record that was written
//   - headModRev: the mod revision of the new head
func UpdateVerifiedState(idx uint64, digest []byte, genesisHash []byte, head *HashChainHead, headModRev int64) {
	chainWatcher.mu.Lock()
	defer chainWatcher.mu.Unlock()

	if len(genesisHash) > 0 && len(chainWatcher.genesisHash) == 0 {
		chainWatcher.genesisHash = genesisHash
	}
	chainWatcher.updateVerifiedStateLocked(idx, digest, headModRev, head)
}

// UpdateWatcherWithVerifiedEntries updates the watcher state with entries and data records
// from a VerifyChainResult. This is called by StoreEnclavePublicKeyWithHashChainVerified
// to sync the watcher state after verification, matching what catchUpHashChain does.
// Returns true if the verifiedIdx was updated, false if skipped due to stale result.
func UpdateWatcherWithVerifiedEntries(result *VerifyChainResult) bool {
	logDev := mutil.LogWithPrefix("dev - UpdateWatcherWithVerifiedEntries")

	if result == nil || result.Head == nil {
		return false
	}

	chainWatcher.mu.Lock()
	defer chainWatcher.mu.Unlock()

	// Check if this result is stale before doing any work
	if result.VerifiedIdx < chainWatcher.verifiedIdx {
		logDev("Skipping stale update: result.VerifiedIdx=%d < current verifiedIdx=%d",
			result.VerifiedIdx, chainWatcher.verifiedIdx)
		return false
	}

	// Copy verified entries to watcher (safe even for concurrent updates since maps are keyed)
	for idx, entry := range result.Entries {
		chainWatcher.verifiedEntries[idx] = entry
	}
	for dataKey, dataRecord := range result.DataRecords {
		chainWatcher.verifiedDataRecords[dataKey] = dataRecord
	}

	// Update core verified state (will also check for stale idx)
	return chainWatcher.updateVerifiedStateLocked(result.VerifiedIdx, result.VerifiedDigest, result.HeadModRev, result.Head)
}

// updateVerifiedStateLocked updates the core verified state fields.
// Caller MUST hold chainWatcher.mu lock.
// Only updates if the new idx is >= the current verifiedIdx to prevent
// concurrent updates from overwriting newer state with older state.
// Returns true if the state was updated, false if skipped due to stale idx.
func (w *HashChainWatcher) updateVerifiedStateLocked(idx uint64, digest []byte, headModRev int64, head *HashChainHead) bool {
	// Prevent overwriting newer state with older state from concurrent operations
	if idx < w.verifiedIdx {
		return false
	}
	w.verifiedIdx = idx
	w.verifiedDigest = digest
	w.headModRev = headModRev
	if head != nil {
		w.verifiedHeads[idx] = head
	}
	return true
}

// storeVerifiedEntry stores a single verified entry and its data record in the watcher cache.
// Caller MUST hold chainWatcher.mu lock.
func (w *HashChainWatcher) storeVerifiedEntryLocked(entry *HashChainEntry, dataRecord *HashChainDataRecord) {
	if entry != nil {
		w.verifiedEntries[entry.Idx] = entry
	}
	if dataRecord != nil && entry != nil {
		w.verifiedDataRecords[entry.DataKey] = dataRecord
	}
}

// TODO: ensure the flow verification in subsequent function checks the
// TODO: ordered sequence of function ids in the flow

// ============================================================
// Flow Tracking Functions
// ============================================================

// IsFlowVerified checks if a flow has been verified for a specific service.
// Returns (chainIndex, found) where found is true if the flow was processed by this service.
// This is used for replay detection - if found is true, the service already processed this flow.
func IsFlowVerified(flowID, serviceName string) (uint64, bool) {
	chainWatcher.muFlows.RLock()
	defer chainWatcher.muFlows.RUnlock()

	if flowID == "" || serviceName == "" {
		return 0, false
	}

	serviceMap, exists := chainWatcher.verifiedFlows[flowID]
	if !exists {
		return 0, false
	}

	chainIdx, found := serviceMap[serviceName]
	return chainIdx, found
}

// storeVerifiedFlowLocked stores a verified flow record in the watcher's flow tracking map.
// This must be called with muFlows lock held.
func (w *HashChainWatcher) storeVerifiedFlowLocked(flowID, serviceName string, chainIdx uint64) {
	if flowID == "" || serviceName == "" {
		return
	}

	if w.verifiedFlows[flowID] == nil {
		w.verifiedFlows[flowID] = make(map[string]uint64)
	}
	w.verifiedFlows[flowID][serviceName] = chainIdx
}

// processFlowRecordIfNeeded checks if a data key is a flow record and stores it in verifiedFlows.
// This is called after verifying an entry to track flow processing for replay detection.
func processFlowRecordIfNeeded(dataKey string, chainIdx uint64) {
	flowID, serviceName := parseFlowDataKey(dataKey)
	if flowID == "" || serviceName == "" {
		return // Not a flow record
	}

	chainWatcher.muFlows.Lock()
	defer chainWatcher.muFlows.Unlock()

	chainWatcher.storeVerifiedFlowLocked(flowID, serviceName, chainIdx)
}

// processAllVerifiedKeys processes leader, member, or re-encryption keys for a verified data record.
// This is called after chain verification succeeds. It determines the key type from the dataKey
// and only calls the appropriate processing function in a separate goroutine.
func (kr *KeyRegistry) processAllVerifiedKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	// Check if it's a leader key (leaders/<service>/<function>/<keyType>/<pod-id>)
	if parseLeaderKeyPath(dataKey) != nil {
		// Process for "every leader" storage (by service name) - all pods need this
		go kr.processVerifiedEveryLeaderKeys(dataKey, dataRecord, writerID)
		// Process for member's own leader storage (by pod ID) - only members need this
		go kr.processVerifiedLeaderKeys(dataKey, dataRecord, writerID)
		return
	}

	// Check if it's a member key (members/<leader-pod-id>/<keyType>/<member-pod-id>)
	if memberInfo := parseMemberKeyPath(dataKey); memberInfo != nil {
		switch memberInfo.KeyType {
		case "publicKey":
			go kr.processVerifiedMemberKeys(dataKey, dataRecord, writerID)
		case "reEncryptionKey":
			go kr.processVerifiedReEncryptionKeys(dataKey, dataRecord, writerID)
		}
		return
	}

	// Unknown key type - no processing needed
	logDev := mutil.LogWithPrefix("dev - processAllVerifiedKeys")
	logDev("No key processing needed for dataKey: %s", dataKey)
}

// determineKeyTypeForDeferral determines the key type from a data key path
// for the purpose of deferring processing until enclave public key is published.
// Only returns types that write to etcd: "leader" or "member".
// Re-encryption keys don't write to etcd, so they're not returned here.
func determineKeyTypeForDeferral(dataKey string) string {
	if strings.HasPrefix(dataKey, "leaders/") {
		return "leader"
	}
	if strings.HasPrefix(dataKey, "members/") && strings.Contains(dataKey, "/publicKey/") {
		return "member"
	}
	return ""
}

// isReEncryptionKey checks if a data key is a re-encryption key path.
func isReEncryptionKey(dataKey string) bool {
	return strings.HasPrefix(dataKey, "members/") && strings.Contains(dataKey, "/reEncryptionKey/")
}

// LeaderKeyInfo contains parsed information from a leader key data key
type LeaderKeyInfo struct {
	ServiceName string
	FunctionID  string
	KeyType     string // "publicKey" or "publicParams"
	LeaderPodID string
}

// parseLeaderKeyPath parses a leader key data key path.
// Expected format: leaders/<service>/<function>/publicKey/<pod-id>
//
//	or: leaders/<service>/<function>/publicParams/<pod-id>
//
// Returns nil if the path doesn't match the expected format.
func parseLeaderKeyPath(dataKey string) *LeaderKeyInfo {
	if !strings.HasPrefix(dataKey, "leaders/") {
		return nil
	}

	// Remove "leaders/" prefix
	rest := strings.TrimPrefix(dataKey, "leaders/")

	// Split the remaining path: <service>/<function>/<keyType>/<pod-id>
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return nil
	}

	keyType := parts[2]
	if keyType != "publicKey" && keyType != "publicParams" {
		return nil
	}

	return &LeaderKeyInfo{
		ServiceName: parts[0],
		FunctionID:  parts[1],
		KeyType:     keyType,
		LeaderPodID: parts[3],
	}
}

// processVerifiedLeaderKeys checks if a verified entry contains leader public keys or params
// and processes them. This replaces the old ListWatchLeaderKeys approach.
// After verification succeeds in handleEntryEvent(), this method is called to process
// entries that match the leader key pattern.
//
// For publicKey entries: deserialize and store via SafeWriteMemLeaderPublicKey
// For publicParams entries: deserialize, store, generate member keypair, and store member's public key
//
// NOTE: If our enclave public key hasn't been published yet, we defer processing to the pending queue.
// This is because publicParams processing writes our member public key to the chain, and other pods
// can't verify our signature until our enclave public key is published.
func (kr *KeyRegistry) processVerifiedLeaderKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedLeaderKeys")

	// Parse the data key to see if it's a leader key
	keyInfo := parseLeaderKeyPath(dataKey)
	if keyInfo == nil {
		// Not a leader key entry, nothing to do
		return
	}

	// Only members should process leader keys - leader already has its own keys
	if kr.StartedLeading.Load() {
		logDev("I am the leader, skipping leader key processing for %s", dataKey)
		return
	}

	// Only process leader keys from my leader
	myLeaderId := kr.SafeReadMemLeaderId()
	if myLeaderId != "" && keyInfo.LeaderPodID != myLeaderId {
		logDev("Leader key is from %s, not my leader (%s), skipping", keyInfo.LeaderPodID, myLeaderId)
		return
	}

	// Check if our enclave public key has been published.
	// If not, we must defer processing because when we store our member public key,
	// other pods won't be able to verify the signature (they don't have our public key yet).
	if !IsEnclavePublicKeyPublished() {
		logDev("Enclave public key not yet published, deferring leader key processing for %s", dataKey)
		addPendingKeyRecord(dataKey, dataRecord, writerID, "leader")
		return
	}

	// Call the internal processing function
	kr.processVerifiedLeaderKeysInternal(dataKey, dataRecord, writerID)
}

// processVerifiedLeaderKeysInternal performs the actual leader key processing.
// This is called either directly (if enclave public key is already published)
// or from ProcessPendingKeyRecords (for deferred records).
func (kr *KeyRegistry) processVerifiedLeaderKeysInternal(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedLeaderKeysInternal")

	// Re-parse the key info (needed for deferred processing)
	keyInfo := parseLeaderKeyPath(dataKey)
	if keyInfo == nil {
		logDev("Failed to re-parse leader key path: %s", dataKey)
		return
	}

	logDev("=== RECEIVED VERIFIED LEADER KEY FROM HASH CHAIN ===")
	logDev("  DataKey: %s", dataKey)
	logDev("  ServiceName: %s", keyInfo.ServiceName)
	logDev("  FunctionID: %s", keyInfo.FunctionID)
	logDev("  KeyType: %s", keyInfo.KeyType)
	logDev("  LeaderPodID: %s", keyInfo.LeaderPodID)
	logDev("  WriterID: %s", writerID)
	logDev("  PayloadSize: %d bytes", len(dataRecord.Payload))

	leaderPodId := keyInfo.LeaderPodID

	// Handle publicKey entries
	if keyInfo.KeyType == "publicKey" {
		pks := new(samba.PublicKeySerialized)
		err := json.Unmarshal(dataRecord.Payload, pks)
		if err != nil {
			logDev("Failed to decode leader public key: %v", err)
			return
		}

		publicKey, err := pks.DeSerialize()
		if err != nil {
			logDev("Failed to deserialize leader public key: %v", err)
			return
		}

		kr.SafeWriteMemLeaderPublicKey(leaderPodId, publicKey)
		logDev("Stored leader public key for leaderPodId=%s (from hash chain)", leaderPodId)
	}

	// Handle publicParams entries
	if keyInfo.KeyType == "publicParams" {
		pps := new(samba.PublicParamsSerialized)
		err := json.Unmarshal(dataRecord.Payload, pps)
		if err != nil {
			logDev("Failed to decode leader public params: %v", err)
			return
		}

		publicParams, err := pps.DeSerialize()
		if err != nil {
			logDev("Failed to deserialize leader public params: %v", err)
			return
		}

		kr.SafeWriteMemLeaderPublicParams(leaderPodId, publicParams)
		logDev("Stored leader public params for leaderPodId=%s (from hash chain)", leaderPodId)

		// Generate member keypair using leader's public params
		// First try to read a static keypair from environment variable
		var keyPair *pre.KeyPair
		memberKeyPairString := os.Getenv("MEMBER_KP")
		if memberKeyPairString != "" {
			keyPair, err = samba.ParseKeyPair([]byte(memberKeyPairString))
			if err != nil {
				logDev("Failed to parse static member key pair: %v", err)
			} else {
				logDev("Parsed static member key pair successfully")
			}
		}

		if keyPair == nil {
			logDev("Generating new key pair for member using leader's public params")
			keyPair = pre.KeyGen(publicParams)
		}

		kr.SafeWriteMemKeyPair(leaderPodId, keyPair)
		logDev("Created key pair for member %s", kr.PodId)

		// Store member's public key under members/<leader-pod-id>/publicKey/<member-pod-id>
		// Use hash chain storage for tamper-evident verification
		// Must serialize curve points properly before JSON marshaling
		memPubKeyLabel := "members/" + leaderPodId + "/publicKey/" + kr.PodId
		memberPks := new(samba.PublicKeySerialized)
		memberPks.Serialize(keyPair.PK)
		err = kr.StoreWithHashChainAndRetry(memPubKeyLabel, memberPks, 100)
		if err != nil {
			logDev("Failed to store member public key with hash chain: %v", err)
			return
		}
		logDev("Stored member public key with hash chain at %s", memPubKeyLabel)
	}

	logDev("=== END LEADER KEY PROCESSING ===")
}

// processVerifiedEveryLeaderKeys processes leader public keys and params for ALL leaders.
// This replaces the old ListWatchEveryLeaderPublicKeys approach.
// Unlike processVerifiedLeaderKeys (which stores by pod ID for member's own leader),
// this stores by service name so all functions can discover all leaders.
//
// This is called for ALL leader keys, regardless of whether this pod is a leader or member.
// It does NOT write to etcd, so no deferral is needed.
func (kr *KeyRegistry) processVerifiedEveryLeaderKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedEveryLeaderKeys")

	// Parse the data key to see if it's a leader key
	keyInfo := parseLeaderKeyPath(dataKey)
	if keyInfo == nil {
		// Not a leader key entry, nothing to do
		return
	}

	logDev("=== RECEIVED VERIFIED LEADER KEY FOR EVERY-LEADER STORAGE ==")
	logDev("  DataKey: %s", dataKey)
	logDev("  ServiceName: %s", keyInfo.ServiceName)
	logDev("  FunctionID: %s", keyInfo.FunctionID)
	logDev("  KeyType: %s", keyInfo.KeyType)
	logDev("  LeaderPodID: %s", keyInfo.LeaderPodID)
	logDev("  WriterID: %s", writerID)
	logDev("  PayloadSize: %d bytes", len(dataRecord.Payload))

	leaderServiceName := keyInfo.ServiceName

	// Handle publicKey entries
	if keyInfo.KeyType == "publicKey" {
		pks := new(samba.PublicKeySerialized)
		err := json.Unmarshal(dataRecord.Payload, pks)
		if err != nil {
			logDev("Failed to decode leader public key: %v", err)
			return
		}

		publicKey, err := pks.DeSerialize()
		if err != nil {
			logDev("Failed to deserialize leader public key: %v", err)
			return
		}

		kr.SafeWriteEveryLeaderPublicKey(leaderServiceName, publicKey)
		logDev("Stored every-leader public key for service=%s (from hash chain)", leaderServiceName)
	}

	// Handle publicParams entries
	if keyInfo.KeyType == "publicParams" {
		pps := new(samba.PublicParamsSerialized)
		err := json.Unmarshal(dataRecord.Payload, pps)
		if err != nil {
			logDev("Failed to decode leader public params: %v", err)
			return
		}

		publicParams, err := pps.DeSerialize()
		if err != nil {
			logDev("Failed to deserialize leader public params: %v", err)
			return
		}

		kr.SafeWriteEveryLeaderPublicParams(leaderServiceName, publicParams)
		logDev("Stored every-leader public params for service=%s (from hash chain)", leaderServiceName)
	}

	logDev("=== END EVERY-LEADER KEY PROCESSING ===")
}

// MemberKeyInfo contains parsed information from a member key data key
type MemberKeyInfo struct {
	LeaderPodID string
	KeyType     string // "publicKey" or "reEncryptionKey"
	MemberPodID string
}

// parseMemberKeyPath parses a member key data key path.
// Expected format: members/<leader-pod-id>/publicKey/<member-pod-id>
//
//	or: members/<leader-pod-id>/reEncryptionKey/<member-pod-id>
//
// Returns nil if the path doesn't match the expected format.
func parseMemberKeyPath(dataKey string) *MemberKeyInfo {
	if !strings.HasPrefix(dataKey, "members/") {
		return nil
	}

	// Remove "members/" prefix
	rest := strings.TrimPrefix(dataKey, "members/")

	// Split the remaining path: <leader-pod-id>/<keyType>/<member-pod-id>
	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return nil
	}

	keyType := parts[1]
	if keyType != "publicKey" && keyType != "reEncryptionKey" {
		return nil
	}

	return &MemberKeyInfo{
		LeaderPodID: parts[0],
		KeyType:     keyType,
		MemberPodID: parts[2],
	}
}

// processVerifiedMemberKeys checks if a verified entry contains member public keys
// and processes them. This replaces the old ListWatchMemberPublicKeys approach.
// After verification succeeds in handleEntryEvent(), this method is called to process
// entries that match the member key pattern.
//
// For publicKey entries (members/<leader-pod-id>/publicKey/<member-pod-id>):
// - Only the leader should process these (to generate re-encryption keys)
// - Deserialize the member's public key
// - Generate a re-encryption key using leader's secret key
// - Store the re-encryption key for the member
func (kr *KeyRegistry) processVerifiedMemberKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedMemberKeys")

	// Parse the data key to see if it's a member key
	keyInfo := parseMemberKeyPath(dataKey)
	if keyInfo == nil {
		// Not a member key entry, nothing to do
		return
	}

	// Only process publicKey entries (we generate re-encryption keys from these)
	if keyInfo.KeyType != "publicKey" {
		return
	}

	// Only the leader should process member public keys to generate re-encryption keys
	if !kr.StartedLeading.Load() {
		logDev("Not a leader, skipping member public key processing for %s", dataKey)
		return
	}

	// Check if this member public key is for me (the leader)
	if keyInfo.LeaderPodID != kr.PodId {
		logDev("Member public key is for leader %s, not me (%s), skipping", keyInfo.LeaderPodID, kr.PodId)
		return
	}

	// Check if our enclave public key has been published.
	// If not, we must defer processing because when we store the re-encryption key,
	// other pods won't be able to verify the signature (they don't have our public key yet).
	if !IsEnclavePublicKeyPublished() {
		logDev("Enclave public key not yet published, deferring member key processing for %s", dataKey)
		addPendingKeyRecord(dataKey, dataRecord, writerID, "member")
		return
	}

	// Call the internal processing function
	kr.processVerifiedMemberKeysInternal(dataKey, dataRecord, writerID)
}

// processVerifiedMemberKeysInternal performs the actual member key processing.
// This is called either directly (if enclave public key is already published)
// or from ProcessPendingMemberKeys (for deferred records).
func (kr *KeyRegistry) processVerifiedMemberKeysInternal(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedMemberKeysInternal")

	// Re-parse the key info (needed for deferred processing)
	keyInfo := parseMemberKeyPath(dataKey)
	if keyInfo == nil {
		logDev("Failed to re-parse member key path: %s", dataKey)
		return
	}

	logDev("=== RECEIVED VERIFIED MEMBER PUBLIC KEY FROM HASH CHAIN ===")
	logDev("  DataKey: %s", dataKey)
	logDev("  LeaderPodID: %s", keyInfo.LeaderPodID)
	logDev("  MemberPodID: %s", keyInfo.MemberPodID)
	logDev("  WriterID: %s", writerID)
	logDev("  PayloadSize: %d bytes", len(dataRecord.Payload))

	memberPodId := keyInfo.MemberPodID

	// Deserialize the member's public key from the payload
	pks := new(samba.PublicKeySerialized)
	err := json.Unmarshal(dataRecord.Payload, pks)
	if err != nil {
		logDev("Failed to decode member public key: %v", err)
		return
	}

	publicKey, err := pks.DeSerialize()
	if err != nil {
		logDev("Failed to deserialize member public key: %v", err)
		return
	}

	// Store the member's public key in LeaMemPublicKeys map
	lmMap := kr.LeaMemPublicKeys
	if lmMap == nil {
		lmMap = make(map[string]*pre.PublicKey)
		kr.LeaMemPublicKeys = lmMap
	}
	lmMap[memberPodId] = publicKey
	logDev("Stored public key for member %s", memberPodId)

	// Create a re-encryption key for the member using leader's keys
	pp, kp := kr.SafeReadLeaderKeys()
	if pp == nil || kp == nil {
		logDev("Leader keys not available, cannot generate re-encryption key")
		return
	}
	reEncryptionKey := pre.ReEncryptionKeyGen(pp, kp.SK, publicKey)

	// Store the re-encryption key in LeaMemReEncryptionKeys map
	rKeyMap := kr.LeaMemReEncryptionKeys
	if rKeyMap == nil {
		rKeyMap = make(map[string]*pre.ReEncryptionKey)
		kr.LeaMemReEncryptionKeys = rKeyMap
	}
	rKeyMap[memberPodId] = reEncryptionKey
	logDev("Created re-encryption key for member %s", memberPodId)

	// Store member's re-encryption key in etcd with hash chain
	// Path: members/<leader-pod-id>/reEncryptionKey/<member-pod-id>
	memReEncKeyLabel := "members/" + kr.PodId + "/reEncryptionKey/" + memberPodId

	// Serialize the re-encryption key properly before storing
	reks := new(samba.ReEncryptionKeySerialized)
	reks.Serialize(reEncryptionKey)
	err = kr.StoreWithHashChainAndRetry(memReEncKeyLabel, reks, 100)
	if err != nil {
		logDev("Failed to store member re-encryption key with hash chain: %v", err)
		return
	}
	logDev("Stored member re-encryption key with hash chain at %s", memReEncKeyLabel)

	logDev("=== END MEMBER KEY PROCESSING ===")
}

// processVerifiedReEncryptionKeys checks if a verified entry contains a re-encryption key
// and processes it. This replaces the old ListWatchReEncryptionKey approach.
// After verification succeeds in handleEntryEvent(), this method is called to process
// entries that match the re-encryption key pattern.
//
// For reEncryptionKey entries (members/<leader-pod-id>/reEncryptionKey/<member-pod-id>):
// - Only the member should process these (to receive re-encryption keys from leader)
// - Deserialize the re-encryption key
// - Store it via SafeWriteMemLeaderReEncryptionKey()
// - Mark the pod as PRE-ready
func (kr *KeyRegistry) processVerifiedReEncryptionKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedReEncryptionKeys")

	// Parse the data key to see if it's a member key
	keyInfo := parseMemberKeyPath(dataKey)
	if keyInfo == nil {
		// Not a member key entry, nothing to do
		return
	}

	// Only process reEncryptionKey entries
	if keyInfo.KeyType != "reEncryptionKey" {
		return
	}

	// Only members should process re-encryption keys - leader generates them
	if kr.StartedLeading.Load() {
		logDev("I am the leader, skipping re-encryption key processing for %s", dataKey)
		return
	}

	// Check if this re-encryption key is for me (the member)
	if keyInfo.MemberPodID != kr.PodId {
		logDev("Re-encryption key is for member %s, not me (%s), skipping", keyInfo.MemberPodID, kr.PodId)
		return
	}

	logDev("=== RECEIVED VERIFIED RE-ENCRYPTION KEY FROM HASH CHAIN ===")
	logDev("  DataKey: %s", dataKey)
	logDev("  LeaderPodID: %s", keyInfo.LeaderPodID)
	logDev("  MemberPodID: %s", keyInfo.MemberPodID)
	logDev("  WriterID: %s", writerID)
	logDev("  PayloadSize: %d bytes", len(dataRecord.Payload))

	leaderPodId := keyInfo.LeaderPodID

	// Deserialize the re-encryption key from the payload
	rks := new(samba.ReEncryptionKeySerialized)
	err := json.Unmarshal(dataRecord.Payload, rks)
	if err != nil {
		logDev("Failed to decode re-encryption key: %v", err)
		return
	}

	reEncryptionKey, err := rks.DeSerialize()
	if err != nil {
		logDev("Failed to deserialize re-encryption key: %v", err)
		return
	}

	// Store the re-encryption key via SafeWriteMemLeaderReEncryptionKey
	kr.SafeWriteMemLeaderReEncryptionKey(leaderPodId, reEncryptionKey)
	logDev("Stored re-encryption key from leader %s", leaderPodId)

	// Mark the pod as PRE-ready since we now have the re-encryption key
	go kr.MarkPodPreReady()

	logDev("=== END RE-ENCRYPTION KEY PROCESSING ===")
}
