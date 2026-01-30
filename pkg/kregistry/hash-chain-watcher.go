package kregistry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/etclab/pre"
	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"
)

// ============================================================
// Watch-Based Hash Chain Verification
// ============================================================

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
}

// Package-level watcher state
var chainWatcher = &HashChainWatcher{
	verifiedHeads:       make(map[uint64]*HashChainHead),
	verifiedEntries:     make(map[uint64]*HashChainEntry),
	verifiedDataRecords: make(map[string]*HashChainDataRecord),
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

// catchUpHashChain fetches and verifies existing entries up to current head.
// Returns the head's ModRevision for starting the watch.
func (kr *KeyRegistry) catchUpHashChain(ctx context.Context) (int64, error) {
	logDev := mutil.LogWithPrefix("dev - catchUpHashChain")

	// Get current head
	head, headModRev, err := kr.GetHashChainHead(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get hash chain head: %w", err)
	}

	if head == nil {
		logDev("No hash chain head yet, starting fresh")
		return 0, nil
	}

	chainWatcher.mu.RLock()
	nextIdToVerify := chainWatcher.verifiedIdx + 1
	prevDigest := chainWatcher.verifiedDigest
	chainWatcher.mu.RUnlock()

	// Initialize from genesis if needed
	if nextIdToVerify == 1 || prevDigest == nil {
		nextIdToVerify = 1
		prevDigest = chainWatcher.genesisHash
		if len(prevDigest) == 0 {
			logDev("Warning: No genesis hash provided, using zero hash")
			prevDigest = make([]byte, 32)
		}
	}

	logDev("Catching up from idx=%d to head.Idx=%d", nextIdToVerify, head.Idx)

	// Verify entries from startIdx to head.Idx
	// starting with nextIdxToVerify verify upto <= head.Idx
	for i := nextIdToVerify; i <= head.Idx; i++ {
		entry, err := kr.getEntry(ctx, HashChainEntryPrefix+strconv.FormatUint(i, 10))
		if err != nil {
			return 0, fmt.Errorf("failed to fetch entry %d: %w", i, err)
		}

		dataRecord, err := kr.verifyAndProcessEntryWithData(ctx, entry, prevDigest)
		if err != nil {
			return 0, err
		}

		// Compute new digest
		entrySignHash := computeEntrySignatureMessage(entry)
		prevDigest = computeNewDigest(prevDigest, entrySignHash, entry)

		// Store verified entry and data record
		chainWatcher.mu.Lock()
		chainWatcher.verifiedEntries[entry.Idx] = entry
		if dataRecord != nil {
			chainWatcher.verifiedDataRecords[entry.DataKey] = dataRecord
		}
		chainWatcher.mu.Unlock()

		// Process leader keys from catch-up entries (non-blocking)
		// This is independent of chain validation and can run in a separate goroutine
		if dataRecord != nil {
			go kr.processVerifiedLeaderKeys(entry.DataKey, dataRecord, entry.WriterID)
		}
	}

	// Verify head signature and digest match
	if err := kr.verifyHead(ctx, head, prevDigest); err != nil {
		return 0, err
	}

	// Update watcher state including verified head
	chainWatcher.mu.Lock()
	chainWatcher.verifiedIdx = head.Idx
	chainWatcher.verifiedDigest = prevDigest
	chainWatcher.headModRev = headModRev
	chainWatcher.verifiedHeads[head.Idx] = head
	chainWatcher.mu.Unlock()

	// Seal state for persistence
	if kr.PodId != "" && kr.FunctionId != "" {
		if err := sealVerifiedState(kr.PodId, kr.FunctionId, head.Idx, prevDigest); err != nil {
			logDev("Warning: failed to seal verified state: %v", err)
		}
	}

	logDev("Caught up to idx=%d", head.Idx)
	return headModRev, nil
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

// handleEntryEvent processes an incoming entry event from the watch.
// It verifies the entry signature, fetches/verifies the data record and head in a single transaction.
// The entry, data, and head are created together in a transaction, so they share the same WriterID.
// Verifications are done in parallel for performance.
// If the entry was written by this pod, skip verification since we already verified during store.
func (kr *KeyRegistry) handleEntryEvent(ctx context.Context, key, value []byte, modRevision int64) {
	logDev := mutil.LogWithPrefix("dev - handleEntryEvent")

	logDev("Received entry event for key: %s, modRevision: %d", string(key), modRevision)

	var entry HashChainEntry
	if err := json.Unmarshal(value, &entry); err != nil {
		logDev("Error unmarshaling entry: %v", err)
		return
	}

	chainWatcher.mu.Lock()
	defer chainWatcher.mu.Unlock()

	prevDigest := chainWatcher.verifiedDigest
	if prevDigest == nil {
		prevDigest = chainWatcher.genesisHash
		if len(prevDigest) == 0 {
			prevDigest = make([]byte, 32)
		}
	}

	// Compute newDigest early (needed for head verification, doesn't depend on verification results)
	entrySignHash := computeEntrySignatureMessage(&entry)
	newDigest := computeNewDigest(prevDigest, entrySignHash, &entry)

	// Get writer's public key once - entry, data, and head all share the same writerID
	// since they are created in the same transaction by the same writer
	writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
	if err != nil {
		logDev("Failed to get writer public key for %s: %v", entry.WriterID, err)
		return
	}

	// Error channel for collecting results from 3 verification goroutines
	errCh := make(chan error, 3)

	// Goroutine 1: Verify entry (chain link + signature)
	go func() {
		// Check chain link
		if !bytes.Equal(entry.PrevDigest, prevDigest) {
			errCh <- fmt.Errorf("chain broken at idx %d: prev_digest mismatch", entry.Idx)
			return
		}
		// Verify entry signature
		if err := verifyEntrySignature(&entry, writerPubKey); err != nil {
			errCh <- fmt.Errorf("entry signature verification failed: %w", err)
			return
		}
		errCh <- nil
	}()

	// Main body: Fetch both data record and head in a single transaction
	// Pass the modRevision so we fetch the head at the exact point when this entry was written
	dataRecord, head, headModRev, err := kr.fetchDataAndHead(ctx, entry.DataKey, modRevision)
	if err != nil {
		logDev("Failed to fetch data and head: %v", err)
		// Drain the entry verification goroutine
		<-errCh
		return
	}

	// Goroutine 2: Verify data record
	go func() {
		// Verify payload hash
		payloadHash := sha256.Sum256(dataRecord.Payload)
		if !bytes.Equal(payloadHash[:], entry.ValueHash) {
			errCh <- fmt.Errorf("payload hash mismatch at idx %d", entry.Idx)
			return
		}
		// Verify data signature (uses same writerPubKey)
		dataSignMsg := computeDataSignatureMessage(entry.DataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)
		if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
			errCh <- fmt.Errorf("data signature verification failed at %s", entry.DataKey)
			return
		}
		errCh <- nil
	}()

	// Goroutine 3: Verify head
	go func() {
		// Verify head digest matches our computed newDigest
		if !bytes.Equal(head.Digest, newDigest) {
			errCh <- fmt.Errorf("head digest mismatch (expected %x, got %x)", newDigest, head.Digest)
			return
		}
		// Verify head signature (uses same writerPubKey)
		if err := VerifyHeadSignature(head, writerPubKey); err != nil {
			errCh <- fmt.Errorf("head signature verification failed: %w", err)
			return
		}
		errCh <- nil
	}()

	// Wait for all 3 verification goroutines to complete
	for i := 0; i < 3; i++ {
		if err := <-errCh; err != nil {
			logDev("Verification failed: %v", err)
			// Drain remaining goroutines
			for j := i + 1; j < 3; j++ {
				<-errCh
			}
			return
		}
	}

	// All verifications passed - update state and store verified entry/data/head
	chainWatcher.verifiedIdx = entry.Idx
	chainWatcher.verifiedDigest = newDigest
	chainWatcher.headModRev = headModRev
	chainWatcher.verifiedEntries[entry.Idx] = &entry
	chainWatcher.verifiedHeads[entry.Idx] = head
	chainWatcher.verifiedDataRecords[entry.DataKey] = dataRecord

	// Seal state for persistence
	if kr.PodId != "" && kr.FunctionId != "" {
		if err := sealVerifiedState(kr.PodId, kr.FunctionId, entry.Idx, newDigest); err != nil {
			logDev("Warning: failed to seal verified state: %v", err)
		}
	}

	logDev("Verified entry idx=%d with head, modRev=%d", entry.Idx, headModRev)

	// Process verified entries for leader public keys and params (non-blocking)
	// This is independent of chain validation and can run in a separate goroutine
	// These are stored at: leaders/<service>/<function>/publicKey/<pod-id>
	//                  or: leaders/<service>/<function>/publicParams/<pod-id>
	go kr.processVerifiedLeaderKeys(entry.DataKey, dataRecord, entry.WriterID)
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

// LeaderKeyInfo contains parsed information from a leader key data key
type LeaderKeyInfo struct {
	ServiceName string
	FunctionID  string
	KeyType     string // "publicKey" or "publicParams"
	LeaderPodID string
}

// parseLeaderKeyPath parses a leader key data key path.
// Expected format: leaders/<service>/<function>/publicKey/<pod-id>
//              or: leaders/<service>/<function>/publicParams/<pod-id>
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
func (kr *KeyRegistry) processVerifiedLeaderKeys(dataKey string, dataRecord *HashChainDataRecord, writerID string) {
	logDev := mutil.LogWithPrefix("dev - processVerifiedLeaderKeys")

	// Parse the data key to see if it's a leader key
	keyInfo := parseLeaderKeyPath(dataKey)
	if keyInfo == nil {
		// Not a leader key entry, nothing to do
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
		err = kr.StoreWithHashChainAndRetry(memPubKeyLabel, memberPks, 5)
		if err != nil {
			logDev("Failed to store member public key with hash chain: %v", err)
			return
		}
		logDev("Stored member public key with hash chain at %s", memPubKeyLabel)
	}

	logDev("=== END LEADER KEY PROCESSING ===")
}
