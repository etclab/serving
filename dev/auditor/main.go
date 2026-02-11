// Package main provides a standalone auditor that discovers completed per-flow
// chains, verifies them, and anchors batches onto the global audit chain.
//
// The auditor runs outside the cluster (no SGX) and uses client Ed25519 keys
// from dev/client/ for signing global chain entries. It trusts enclave public
// keys stored in etcd (they were verified by SGX watchers at write time).
//
// Usage:
//
//	go run ./dev/auditor/ \
//	  -etcd-endpoints localhost:2379 \
//	  -keys-dir dev/client \
//	  -function-chain "validate-fun/lookup-fun/respond-fun"
package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/kregistry"
)

// ============================================================
// Flow Anchor Payload Structures (imported from kregistry)
// ============================================================

// FlowAnchorPayload and FlowAnchorEntry are defined in pkg/kregistry/hash-chain-watcher.go
// and reused here to avoid duplication.

// VerifiedFlow holds the result of verifying a single per-flow chain.
type VerifiedFlow struct {
	FlowID     string
	HeadIdx    uint64
	HeadDigest []byte
	FuncNames  []string
}

// ============================================================
// Writer Public Key Cache (no SGX attestation)
// ============================================================

var (
	pubKeyCacheMu sync.RWMutex
	pubKeyCache   = make(map[string]ed25519.PublicKey)
)

// ============================================================
// Global Chain Verified State (shared between watcher and anchor)
// ============================================================

// VerifiedState tracks the last verified state of the global chain
// to avoid redundant verification and re-fetching. It also maintains
// the set of anchored flows discovered during verification.
type VerifiedState struct {
	mu             sync.RWMutex
	idx            uint64
	digest         []byte
	head           *kregistry.HashChainHead // nil if no head yet (empty chain)
	headModRev     int64
	anchoredFlows  map[string]bool // flows that have been anchored
	lastScannedRev int64           // last etcd revision scanned for flow completeness
}

func (vs *VerifiedState) Get() (idx uint64, digest []byte, head *kregistry.HashChainHead, headModRev int64) {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.idx, vs.digest, vs.head, vs.headModRev
}

func (vs *VerifiedState) Update(idx uint64, digest []byte, head *kregistry.HashChainHead, headModRev int64) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	vs.idx = idx
	vs.digest = digest
	vs.head = head
	vs.headModRev = headModRev
}

func (vs *VerifiedState) IsAnchored(flowID string) bool {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.anchoredFlows[flowID]
}

func (vs *VerifiedState) MarkAnchored(flowIDs []string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	for _, flowID := range flowIDs {
		vs.anchoredFlows[flowID] = true
	}
}

func (vs *VerifiedState) UnmarkAnchored(flowIDs []string) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	for _, flowID := range flowIDs {
		delete(vs.anchoredFlows, flowID)
	}
}

func (vs *VerifiedState) GetLastScannedRev() int64 {
	vs.mu.RLock()
	defer vs.mu.RUnlock()
	return vs.lastScannedRev
}

func (vs *VerifiedState) UpdateLastScannedRev(rev int64) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if rev > vs.lastScannedRev {
		vs.lastScannedRev = rev
	}
}

// getWriterPublicKeyNoAttestation fetches a writer's Ed25519 public key from etcd
// without verifying SGX attestation. The auditor trusts the global chain's
// integrity (keys were CAS-written and verified by SGX watchers).
// Special case: if writerID is "auditor", returns the client's public key from cache.
func getWriterPublicKeyNoAttestation(client *clientv3.Client, writerID string) (ed25519.PublicKey, error) {
	// TODO: later on verify the attestation
	pubKeyCacheMu.RLock()
	if cached, ok := pubKeyCache[writerID]; ok {
		pubKeyCacheMu.RUnlock()
		return cached, nil
	}
	pubKeyCacheMu.RUnlock()

	// Special case: "auditor" writerID uses the client's public key (no etcd lookup needed)
	// The client is working as the auditor, so return the cached auditor public key
	if writerID == "auditor" {
		pubKeyCacheMu.RLock()
		defer pubKeyCacheMu.RUnlock()
		if auditorKey, ok := pubKeyCache["auditor"]; ok {
			return auditorKey, nil
		}
		return nil, fmt.Errorf("auditor public key not found in cache")
	}

	dataKey := kregistry.EnclaveKeysPrefix + writerID + kregistry.EnclavePublicKeySuffix

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Get(ctx, dataKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get writer public key for %s: %w", writerID, err)
	}
	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("writer public key not found for %s", writerID)
	}

	var dataRecord kregistry.HashChainDataRecord
	if err := json.Unmarshal(resp.Kvs[0].Value, &dataRecord); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data record for %s: %w", writerID, err)
	}

	var attestedKey kregistry.AttestedPublicKey
	if err := json.Unmarshal(dataRecord.Payload, &attestedKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal attested key for %s: %w", writerID, err)
	}

	if len(attestedKey.PublicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key size for %s: expected %d, got %d",
			writerID, ed25519.PublicKeySize, len(attestedKey.PublicKey))
	}

	pubKey := ed25519.PublicKey(attestedKey.PublicKey)

	pubKeyCacheMu.Lock()
	pubKeyCache[writerID] = pubKey
	pubKeyCacheMu.Unlock()

	return pubKey, nil
}

// ============================================================
// Key Loading Utilities
// ============================================================

func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from %s", path)
	}

	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key: %w", err)
	}

	ed25519Key, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("key is not an Ed25519 private key")
	}

	return ed25519Key, nil
}

func loadPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read public key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block from %s", path)
	}

	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	ed25519Key, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("key is not an Ed25519 public key")
	}

	return ed25519Key, nil
}

func loadGenesisHash(keysDir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(keysDir, "genesis.hash"))
	if err != nil {
		return nil, fmt.Errorf("failed to read genesis.hash: %w", err)
	}
	hashBytes, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil {
		return nil, fmt.Errorf("failed to decode genesis hash hex: %w", err)
	}
	return hashBytes, nil
}

// ============================================================
// Flow Completeness Scanning
// ============================================================

// scanCompletedFlows discovers per-flow chains that are complete (head.Idx == expectedLen-1)
// and not yet anchored. It uses the last scanned revision to only fetch flows modified since
// the last scan, improving efficiency.
func scanCompletedFlows(client *clientv3.Client, expectedLen int, verifiedState *VerifiedState) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lastScannedRev := verifiedState.GetLastScannedRev()

	// Range query for flow head keys modified after lastScannedRev
	// Use WithMinModRev to only get keys that were modified after our last scan
	opts := []clientv3.OpOption{
		clientv3.WithPrefix(),
		clientv3.WithKeysOnly(),
	}
	if lastScannedRev > 0 {
		opts = append(opts, clientv3.WithMinModRev(lastScannedRev+1))
	}

	resp, err := client.Get(ctx, kregistry.FlowChainPrefix, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list flow keys: %w", err)
	}

	// Track the maximum revision seen in this scan
	var maxRevSeen int64

	// Extract unique flow IDs from head keys
	flowIDs := make(map[string]struct{})
	for _, kv := range resp.Kvs {
		if kv.ModRevision > maxRevSeen {
			maxRevSeen = kv.ModRevision
		}

		key := string(kv.Key)
		if !strings.HasSuffix(key, "/head") {
			continue
		}
		// key = "lambada/flow/<flow_id>/head"
		trimmed := strings.TrimPrefix(key, kregistry.FlowChainPrefix)
		flowID := strings.TrimSuffix(trimmed, "/head")
		if flowID == "" {
			continue
		}
		if verifiedState.IsAnchored(flowID) {
			continue
		}
		flowIDs[flowID] = struct{}{}
	}

	// if lastScannedRev > 0 {
	// 	log.Printf("[scan] discovered %d flows with heads modified since rev %d", len(flowIDs), lastScannedRev)
	// }

	// Check each flow's head for completeness
	var completed []string
	for flowID := range flowIDs {
		headKey := kregistry.FlowHeadKey(flowID)
		headResp, err := client.Get(ctx, headKey)
		if err != nil {
			log.Printf("[scan] failed to get head for flow %s: %v", flowID, err)
			continue
		}
		if len(headResp.Kvs) == 0 {
			continue
		}

		// Track the revision of this head fetch as well
		if len(headResp.Kvs) > 0 && headResp.Kvs[0].ModRevision > maxRevSeen {
			maxRevSeen = headResp.Kvs[0].ModRevision
		}

		var head kregistry.FlowHeadRecord
		if err := json.Unmarshal(headResp.Kvs[0].Value, &head); err != nil {
			log.Printf("[scan] failed to unmarshal head for flow %s: %v", flowID, err)
			continue
		}

		log.Printf("[scan] flow %s has head idx %d\n", flowID, head.Idx)

		if head.Idx == uint64(expectedLen-1) {
			completed = append(completed, flowID)
		}
	}

	// Update the last scanned revision if we saw any keys
	if maxRevSeen > 0 {
		verifiedState.UpdateLastScannedRev(maxRevSeen)
	}

	return completed, nil
}

// ============================================================
// Flow Chain Verification (without SGX)
// ============================================================

// verifyFlowChain verifies a per-flow chain without SGX attestation verification.
// It replicates kregistry.VerifyFlowChain logic but uses getWriterPublicKeyNoAttestation.
func verifyFlowChain(client *clientv3.Client, flowID string, genesisHash []byte) (*VerifiedFlow, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 1. Fetch head
	headKey := kregistry.FlowHeadKey(flowID)
	headResp, err := client.Get(ctx, headKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get flow chain head: %w", err)
	}
	if len(headResp.Kvs) == 0 {
		return nil, fmt.Errorf("flow chain head not found for flow %s", flowID)
	}

	var head kregistry.FlowHeadRecord
	if err := json.Unmarshal(headResp.Kvs[0].Value, &head); err != nil {
		return nil, fmt.Errorf("failed to unmarshal flow chain head: %w", err)
	}
	log.Printf("[verifyFlowChain] verifying flow %s with head idx %d\n", flowID, head.Idx)

	// 2. Batch-fetch all entries 0..head.Idx
	startKey := kregistry.FlowEntryKey(flowID, 0)
	endKey := kregistry.FlowEntryKey(flowID, head.Idx+1)

	entryResp, err := client.Get(ctx, startKey, clientv3.WithRange(endKey))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch flow chain entries: %w", err)
	}

	entries := make([]*kregistry.FlowEntryRecord, 0, len(entryResp.Kvs))
	for _, kv := range entryResp.Kvs {
		var entry kregistry.FlowEntryRecord
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal flow entry: %w", err)
		}
		entries = append(entries, &entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Idx < entries[j].Idx
	})

	log.Printf("[verifyFlowChain] fetched %d entries for flow %s\n", len(entries), flowID)

	// 3. Batch-fetch all data records
	dataKeys := make([]string, len(entries))
	for i, entry := range entries {
		dataKeys[i] = entry.DataKey
	}

	dataRecords := make(map[string]*kregistry.FlowDataRecord, len(entries))
	if len(dataKeys) > 0 {
		ops := make([]clientv3.Op, len(dataKeys))
		for i, key := range dataKeys {
			ops[i] = clientv3.OpGet(key)
		}

		txnResp, err := client.Txn(ctx).Then(ops...).Commit()
		if err != nil {
			return nil, fmt.Errorf("failed to batch fetch flow data records: %w", err)
		}

		for i, key := range dataKeys {
			rangeResp := txnResp.Responses[i].GetResponseRange()
			if len(rangeResp.Kvs) == 0 {
				return nil, fmt.Errorf("flow data not found at %s", key)
			}
			var record kregistry.FlowDataRecord
			if err := json.Unmarshal(rangeResp.Kvs[0].Value, &record); err != nil {
				return nil, fmt.Errorf("failed to unmarshal flow data record at %s: %w", key, err)
			}
			dataRecords[record.FuncName] = &record
		}
	}
	log.Printf("[verifyFlowChain] fetched %d data records for flow %s\n", len(dataRecords), flowID)

	// 4. Verify each entry
	prevDigest := genesisHash
	if len(prevDigest) == 0 {
		prevDigest = make([]byte, 32)
	}

	funcNames := make([]string, 0, len(entries))

	for _, entry := range entries {
		writerPubKey, err := getWriterPublicKeyNoAttestation(client, entry.WriterID)
		if err != nil {
			return nil, fmt.Errorf("failed to get writer public key for %s: %w", entry.WriterID, err)
		}

		// Verify entry signature
		entrySignMsg := kregistry.ComputeFlowEntrySignatureMessage(entry)
		if !ed25519.Verify(writerPubKey, entrySignMsg, entry.EntrySig) {
			return nil, fmt.Errorf("flow entry signature verification failed at idx %d", entry.Idx)
		}

		// Verify data signature
		dataRecord, ok := dataRecords[entry.FuncName]
		if !ok {
			return nil, fmt.Errorf("flow data record not found for func %s at idx %d", entry.FuncName, entry.Idx)
		}
		dataSignMsg := kregistry.ComputeFlowDataSignatureMessage(dataRecord)
		if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
			return nil, fmt.Errorf("flow data signature verification failed for func %s", entry.FuncName)
		}

		// Compute chain digest
		prevDigest = kregistry.ComputeFlowDigest(prevDigest, entrySignMsg, entry)
		funcNames = append(funcNames, entry.FuncName)
	}
	log.Printf("[verifyFlowChain] crunched %d entries for flow %s\n", len(entries), flowID)

	// 5. Verify final digest matches head
	if len(entries) > 0 && !bytes.Equal(prevDigest, head.Digest) {
		return nil, fmt.Errorf("flow chain digest mismatch: expected %x, got %x", head.Digest, prevDigest)
	}

	// 6. Verify head signature (signed by last writer)
	headWriterPubKey, err := getWriterPublicKeyNoAttestation(client, entries[len(entries)-1].WriterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get head writer public key: %w", err)
	}
	headSignMsg := kregistry.ComputeFlowHeadSignatureMessage(flowID, head.Idx, head.Digest)
	if !ed25519.Verify(headWriterPubKey, headSignMsg, head.HeadSig) {
		return nil, fmt.Errorf("flow chain head signature verification failed")
	}

	log.Printf("[verifyFlowChain] successfully verified flow %s with head idx %d\n", flowID, head.Idx)

	return &VerifiedFlow{
		FlowID:     flowID,
		HeadIdx:    head.Idx,
		HeadDigest: head.Digest,
		FuncNames:  funcNames,
	}, nil
}

// ============================================================
// Global Chain Verification & Anchoring
// ============================================================

// verifyGlobalChain verifies the global chain from startIdx to head, returning
// the verified head, its modRevision, the final digest, the verified idx, and
// any flow IDs discovered in ANCHOR_FLOWS operations.
// Uses getWriterPublicKeyNoAttestation for writer key lookups.
func verifyGlobalChain(client *clientv3.Client, startIdx uint64, startDigest []byte) (
	head *kregistry.HashChainHead, headModRev int64, verifiedIdx uint64, verifiedDigest []byte, anchoredFlows []string, err error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var discoveredFlows []string

	// Fetch head
	headResp, err := client.Get(ctx, kregistry.HashChainHeadKey)
	if err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to get global chain head: %w", err)
	}
	if len(headResp.Kvs) == 0 {
		// No head yet — return nil head, caller handles first-write
		log.Printf("[verifyGlobalChain] no global chain head found, starting fresh")
		return nil, 0, 0, startDigest, nil, nil
	}

	var h kregistry.HashChainHead
	if err := json.Unmarshal(headResp.Kvs[0].Value, &h); err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to unmarshal global chain head: %w", err)
	}
	headModRev = headResp.Kvs[0].ModRevision

	if h.Idx < startIdx {
		// Head is behind our start — nothing to verify
		return &h, headModRev, startIdx - 1, startDigest, nil, nil
	}

	// Batch-fetch entries startIdx..head.Idx
	entryStartKey := kregistry.FormatEntryKey(startIdx)
	entryEndKey := kregistry.FormatEntryKey(h.Idx + 1)

	// TODO: how big can this range be?
	entryResp, err := client.Get(ctx, entryStartKey, clientv3.WithRange(entryEndKey))
	if err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to fetch global chain entries: %w", err)
	}

	entries := make([]*kregistry.HashChainEntry, 0, len(entryResp.Kvs))
	for _, kv := range entryResp.Kvs {
		var entry kregistry.HashChainEntry
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			return nil, 0, 0, nil, nil, fmt.Errorf("failed to unmarshal global chain entry: %w", err)
		}
		entries = append(entries, &entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Idx < entries[j].Idx
	})

	// Batch-fetch data records
	dataKeys := make([]string, len(entries))
	for i, entry := range entries {
		dataKeys[i] = entry.DataKey
	}

	dataRecords := make(map[string]*kregistry.HashChainDataRecord)
	if len(dataKeys) > 0 {
		ops := make([]clientv3.Op, len(dataKeys))
		for i, key := range dataKeys {
			ops[i] = clientv3.OpGet(key)
		}
		txnResp, err := client.Txn(ctx).Then(ops...).Commit()
		if err != nil {
			return nil, 0, 0, nil, nil, fmt.Errorf("failed to batch fetch data records: %w", err)
		}
		for i, key := range dataKeys {
			rangeResp := txnResp.Responses[i].GetResponseRange()
			if len(rangeResp.Kvs) == 0 {
				return nil, 0, 0, nil, nil, fmt.Errorf("data record not found at %s", key)
			}
			var record kregistry.HashChainDataRecord
			if err := json.Unmarshal(rangeResp.Kvs[0].Value, &record); err != nil {
				return nil, 0, 0, nil, nil, fmt.Errorf("failed to unmarshal data record at %s: %w", key, err)
			}
			dataRecords[key] = &record
		}
	}

	// Verify each entry and extract anchored flows
	prevDigest := startDigest
	for _, entry := range entries {
		// Verify prev_digest chain link
		if !bytes.Equal(entry.PrevDigest, prevDigest) {
			return nil, 0, 0, nil, nil, fmt.Errorf("chain broken at idx %d: prev_digest mismatch", entry.Idx)
		}

		writerPubKey, err := getWriterPublicKeyNoAttestation(client, entry.WriterID)
		if err != nil {
			return nil, 0, 0, nil, nil, fmt.Errorf("failed to get writer key for %s at idx %d: %w", entry.WriterID, entry.Idx, err)
		}

		// Verify entry signature
		entrySignMsg := kregistry.ComputeEntrySignatureMessage(entry)
		if !ed25519.Verify(writerPubKey, entrySignMsg, entry.EntrySig) {
			return nil, 0, 0, nil, nil, fmt.Errorf("entry signature verification failed at idx %d", entry.Idx)
		}

		// Verify data record
		dataRecord := dataRecords[entry.DataKey]
		if dataRecord == nil {
			return nil, 0, 0, nil, nil, fmt.Errorf("data record missing for entry idx %d", entry.Idx)
		}
		payloadHash := sha256.Sum256(dataRecord.Payload)
		if !bytes.Equal(payloadHash[:], entry.ValueHash) {
			return nil, 0, 0, nil, nil, fmt.Errorf("payload hash mismatch at idx %d", entry.Idx)
		}
		dataSignMsg := kregistry.ComputeDataSignatureMessage(entry.DataKey, dataRecord.Idx, payloadHash[:], dataRecord.WriterID)
		if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
			return nil, 0, 0, nil, nil, fmt.Errorf("data signature verification failed at idx %d", entry.Idx)
		}

		// Extract anchored flows from ANCHOR_FLOWS operations
		if entry.OpType == "ANCHOR_FLOWS" {
			var payload kregistry.FlowAnchorPayload
			if err := json.Unmarshal(dataRecord.Payload, &payload); err == nil {
				for _, flow := range payload.AnchoredFlows {
					discoveredFlows = append(discoveredFlows, flow.FlowID)
				}
			}
		}

		// Advance digest
		prevDigest = kregistry.ComputeNewDigest(prevDigest, entrySignMsg, entry)

		log.Printf("[verifyGlobalChain] successfully verified entry idx %d", entry.Idx)
	}

	// Verify head
	if !bytes.Equal(h.Digest, prevDigest) {
		return nil, 0, 0, nil, nil, fmt.Errorf("global chain head digest mismatch: expected %x, got %x", prevDigest, h.Digest)
	}
	headWriterPubKey, err := getWriterPublicKeyNoAttestation(client, h.WriterID)
	if err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to get head writer key: %w", err)
	}
	headSignMsg := kregistry.ComputeHeadSignatureMessage(h.Idx, h.WriterID, h.Digest)
	if !ed25519.Verify(headWriterPubKey, headSignMsg, h.HeadSig) {
		return nil, 0, 0, nil, nil, fmt.Errorf("global chain head signature verification failed")
	}

	return &h, headModRev, h.Idx, prevDigest, discoveredFlows, nil
}

// anchorFlowBatch anchors a batch of verified flows onto the global chain.
// It verifies the global chain, builds a new entry with OpType="ANCHOR_FLOWS",
// and commits via CAS transaction with retry.
func anchorFlowBatch(
	client *clientv3.Client,
	batch []VerifiedFlow,
	signingKey ed25519.PrivateKey,
	writerID string,
	genesisHash []byte,
	verifiedState *VerifiedState,
) error {
	maxAttempts := 20
	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := range maxAttempts {
		err := tryAnchorFlowBatch(client, batch, signingKey, writerID, genesisHash, verifiedState)
		if err == nil {
			return nil
		}

		if strings.Contains(err.Error(), "transaction conflict") {
			sleepDuration := addJitter(backoff)
			log.Printf("[anchor] conflict on attempt %d, retrying after %v: %v", attempt+1, sleepDuration, err)
			time.Sleep(sleepDuration)
			backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
			continue
		}

		return fmt.Errorf("anchor failed on attempt %d: %w", attempt+1, err)
	}
	return fmt.Errorf("anchor failed after %d attempts", maxAttempts)
}

// tryAnchorFlowBatch performs a single attempt at anchoring flows onto the global chain.
// It uses the verified state maintained by watchGlobalChain (no re-verification or re-fetching).
// If the watcher is behind, the CAS transaction will fail and we'll retry.
func tryAnchorFlowBatch(
	client *clientv3.Client,
	batch []VerifiedFlow,
	signingKey ed25519.PrivateKey,
	writerID string,
	genesisHash []byte,
	verifiedState *VerifiedState,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Read verified state from watcher (includes head, no need to fetch)
	verifiedIdx, verifiedDigest, head, headModRev := verifiedState.Get()

	var idx uint64
	var prevDigest []byte
	isFirstWrite := head == nil

	if isFirstWrite {
		// No head yet - first write to global chain
		idx = 1
		prevDigest = genesisHash
		if len(prevDigest) == 0 {
			prevDigest = make([]byte, 32)
		}
	} else {
		// Use verified state from watcher to determine next index
		idx = verifiedIdx + 1
		prevDigest = verifiedDigest
	}

	// 2. Build anchor payload
	anchorEntries := make([]kregistry.FlowAnchorEntry, len(batch))
	for i, vf := range batch {
		anchorEntries[i] = kregistry.FlowAnchorEntry{
			FlowID:     vf.FlowID,
			HeadIdx:    vf.HeadIdx,
			HeadDigest: vf.HeadDigest,
			Functions:  vf.FuncNames,
		}
	}

	payload := kregistry.FlowAnchorPayload{
		AnchoredFlows: anchorEntries,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal anchor payload: %w", err)
	}

	// 3. Build data record
	dataKey := fmt.Sprintf("lambada/audit/anchor-flows/%020d", idx)
	payloadHash := sha256.Sum256(payloadBytes)

	dataSignMsg := kregistry.ComputeDataSignatureMessage(dataKey, idx, payloadHash[:], writerID)
	dataSig := ed25519.Sign(signingKey, dataSignMsg)

	dataRecord := &kregistry.HashChainDataRecord{
		Idx:      idx,
		WriterID: writerID,
		Payload:  payloadBytes,
		DataSig:  dataSig,
	}

	// 4. Build entry record
	entry := &kregistry.HashChainEntry{
		Idx:        idx,
		PrevDigest: prevDigest,
		OpType:     "ANCHOR_FLOWS",
		DataKey:    dataKey,
		ValueHash:  payloadHash[:],
		WriterID:   writerID,
	}

	entrySignMsg := kregistry.ComputeEntrySignatureMessage(entry)
	entry.EntrySig = ed25519.Sign(signingKey, entrySignMsg)

	// 5. Compute new digest
	newDigest := kregistry.ComputeNewDigest(prevDigest, entrySignMsg, entry)

	// 6. Build new head
	headSignMsg := kregistry.ComputeHeadSignatureMessage(idx, writerID, newDigest)
	headSig := ed25519.Sign(signingKey, headSignMsg)

	newHead := &kregistry.HashChainHead{
		Idx:      idx,
		Digest:   newDigest,
		WriterID: writerID,
		HeadSig:  headSig,
	}

	// 7. Marshal records
	dataRecordBytes, err := json.Marshal(dataRecord)
	if err != nil {
		return fmt.Errorf("failed to marshal data record: %w", err)
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}
	headBytes, err := json.Marshal(newHead)
	if err != nil {
		return fmt.Errorf("failed to marshal head: %w", err)
	}

	entryKey := kregistry.FormatEntryKey(idx)

	// 8. CAS transaction
	var txnResp *clientv3.TxnResponse
	if isFirstWrite {
		txnResp, err = client.Txn(ctx).If(
			clientv3.Compare(clientv3.CreateRevision(kregistry.HashChainHeadKey), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(dataKey), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),
		).Then(
			clientv3.OpPut(dataKey, string(dataRecordBytes)),
			clientv3.OpPut(entryKey, string(entryBytes)),
			clientv3.OpPut(kregistry.HashChainHeadKey, string(headBytes)),
		).Commit()
	} else {
		oldHeadBytes, marshalErr := json.Marshal(head)
		if marshalErr != nil {
			return fmt.Errorf("failed to marshal old head: %w", marshalErr)
		}

		log.Printf("[tryAnchorFlowBatch] attempting to anchor at idx=%d with prevDigest=%x", idx, prevDigest)
		log.Printf("[tryAnchorFlowBatch] data keys in transaction: %s, %s, %s", dataKey, entryKey, kregistry.HashChainHeadKey)

		txnResp, err = client.Txn(ctx).If(
			clientv3.Compare(clientv3.Value(kregistry.HashChainHeadKey), "=", string(oldHeadBytes)),
			clientv3.Compare(clientv3.ModRevision(kregistry.HashChainHeadKey), "=", headModRev),
			clientv3.Compare(clientv3.CreateRevision(dataKey), "=", 0),
			clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),
		).Then(
			clientv3.OpPut(dataKey, string(dataRecordBytes)),
			clientv3.OpPut(entryKey, string(entryBytes)),
			clientv3.OpPut(kregistry.HashChainHeadKey, string(headBytes)),
		).Commit()
	}

	if err != nil {
		return fmt.Errorf("transaction failed: %w", err)
	}
	if !txnResp.Succeeded {
		return fmt.Errorf("transaction conflict: head or data key changed")
	}

	return nil
}

// ============================================================
// Global Chain Watcher (background)
// ============================================================

func watchGlobalChain(client *clientv3.Client, genesisHash []byte, verifiedState *VerifiedState) {
	log.Printf("[watcher] starting global chain watcher")

	// Initial verification
	head, headModRev, verifiedIdx, verifiedDigest, anchoredFlows, err := verifyGlobalChain(client, 1, genesisHash)
	if err != nil {
		log.Printf("[watcher] initial verification failed: %v", err)
	} else {
		log.Printf("[watcher] initially verified up to idx=%d", verifiedIdx)
		verifiedState.Update(verifiedIdx, verifiedDigest, head, headModRev)
		if len(anchoredFlows) > 0 {
			verifiedState.MarkAnchored(anchoredFlows)
			log.Printf("[watcher] discovered %d previously anchored flows", len(anchoredFlows))
		} else {
			log.Printf("[watcher] no previously anchored flows discovered")
		}
	}

	lastIdx := verifiedIdx
	lastDigest := verifiedDigest

	ctx := context.Background()
	watchCh := client.Watch(ctx, kregistry.HashChainHeadKey, clientv3.WithRev(headModRev+1))

	for wresp := range watchCh {
		if wresp.Canceled {
			log.Printf("[watcher] watch canceled: %v", wresp.Err())
			return
		}

		for range wresp.Events {
			// Head changed — incrementally verify from last known state
			startIdx := lastIdx + 1
			startDigest := lastDigest
			if startIdx == 1 {
				startDigest = genesisHash
			}

			newHead, newHeadModRev, newIdx, newDigest, newFlows, err := verifyGlobalChain(client, startIdx, startDigest)
			if err != nil {
				log.Printf("[watcher] incremental verification failed from idx=%d: %v", startIdx, err)
				continue
			}

			if newIdx > lastIdx {
				log.Printf("[watcher] verified global chain from idx=%d to idx=%d", lastIdx, newIdx)
				lastIdx = newIdx
				lastDigest = newDigest
				verifiedState.Update(newIdx, newDigest, newHead, newHeadModRev)
				if len(newFlows) > 0 {
					verifiedState.MarkAnchored(newFlows)
					log.Printf("[watcher] marked %d flows as anchored", len(newFlows))
				} else {
					log.Printf("[watcher] no anchored flows discovered in new entries")
				}
			}
		}
	}
}

// ============================================================
// Helpers
// ============================================================

func addJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(d / 2)))
	return d + jitter
}

// ============================================================
// Main
// ============================================================

func main() {
	var (
		etcdEndpoints string
		keysDir       string
		functionChain string
		pollInterval  time.Duration
		batchSize     int
		batchTimeout  time.Duration
		writerID      string
	)

	flag.StringVar(&etcdEndpoints, "etcd-endpoints", "localhost:2379", "Comma-separated etcd endpoints")
	flag.StringVar(&keysDir, "keys-dir", "dev/client", "Directory containing client-pk.pem, client-sk.pem, genesis.hash")
	flag.StringVar(&functionChain, "function-chain", os.Getenv("FUNCTION_CHAIN"), "Slash-separated function chain (e.g. validate-fun/lookup-fun/respond-fun)")
	flag.DurationVar(&pollInterval, "poll-interval", 200*time.Millisecond, "Interval between flow completeness scans")
	flag.IntVar(&batchSize, "batch-size", 5, "Number of flows per anchor batch")
	flag.DurationVar(&batchTimeout, "batch-timeout", 2*time.Second, "Commit partial batch after this timeout")
	flag.StringVar(&writerID, "writer-id", "auditor", "Writer ID for global chain entries")
	flag.Parse()

	if functionChain == "" {
		log.Fatal("function-chain is required (flag or FUNCTION_CHAIN env)")
	}

	functions := strings.Split(functionChain, "/")
	expectedChainLen := len(functions)
	log.Printf("[main] function chain: %v (len=%d)", functions, expectedChainLen)

	// Load keys
	privKey, err := loadPrivateKey(filepath.Join(keysDir, "client-sk.pem"))
	if err != nil {
		log.Fatalf("failed to load private key: %v", err)
	}
	pubKey, err := loadPublicKey(filepath.Join(keysDir, "client-pk.pem"))
	if err != nil {
		log.Fatalf("failed to load public key: %v", err)
	}
	log.Printf("[main] loaded client keys from %s (pubkey: %x...)", keysDir, pubKey[:8])

	genesisHash, err := loadGenesisHash(keysDir)
	if err != nil {
		log.Fatalf("failed to load genesis hash: %v", err)
	}
	log.Printf("[main] genesis hash: %x", genesisHash)

	// Register the auditor's public key in the cache so global chain verification
	// can verify entries written by the auditor itself.
	pubKeyCacheMu.Lock()
	pubKeyCache[writerID] = pubKey
	pubKeyCacheMu.Unlock()

	// Connect to etcd
	endpoints := strings.Split(etcdEndpoints, ",")
	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("failed to connect to etcd: %v", err)
	}
	defer client.Close()

	log.Printf("[main] connected to etcd at %v", endpoints)

	// Initialize shared verified state for global chain
	// The watcher will rebuild the anchored set during initial verification
	verifiedState := &VerifiedState{
		anchoredFlows: make(map[string]bool),
	}

	// Start global chain watcher in background
	go watchGlobalChain(client, genesisHash, verifiedState)

	// Main polling loop
	log.Printf("[main] starting poll loop: interval=%v, batchSize=%d, batchTimeout=%v", pollInterval, batchSize, batchTimeout)

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	batchTimer := time.NewTimer(batchTimeout)
	defer batchTimer.Stop()

	var pendingBatch []VerifiedFlow

	for {
		select {
		case <-ticker.C:
			completedFlowIDs, err := scanCompletedFlows(client, expectedChainLen, verifiedState)
			if err != nil {
				log.Printf("[poll] scan error: %v", err)
				continue
			}
			// log.Printf("[poll] discovered %d completed flows", len(completedFlowIDs))

			for _, flowID := range completedFlowIDs {
				verified, err := verifyFlowChain(client, flowID, genesisHash)
				if err != nil {
					log.Printf("[poll] flow %s verification failed: %v", flowID, err)
					continue
				}
				log.Printf("[poll] verified flow %s (idx=%d, funcs=%v)", flowID, verified.HeadIdx, verified.FuncNames)
				pendingBatch = append(pendingBatch, *verified)
				// Mark as anchored early to avoid re-scanning
				verifiedState.MarkAnchored([]string{flowID})
			}

			if len(pendingBatch) >= batchSize {
				commitBatch := pendingBatch[:batchSize]
				if err := anchorFlowBatch(client, commitBatch, privKey, writerID, genesisHash, verifiedState); err != nil {
					log.Printf("[poll] anchor batch failed: %v", err)
					// Un-mark flows that failed
					failedFlows := make([]string, len(commitBatch))
					for i, vf := range commitBatch {
						failedFlows[i] = vf.FlowID
					}
					verifiedState.UnmarkAnchored(failedFlows)
				} else {
					flowIDs := make([]string, len(commitBatch))
					for i, vf := range commitBatch {
						flowIDs[i] = vf.FlowID
					}
					log.Printf("[poll] anchored batch of %d flows: %v", len(commitBatch), flowIDs)
				}
				pendingBatch = pendingBatch[batchSize:]
				batchTimer.Reset(batchTimeout)
			}

			// log.Printf("[poll] scan complete: %d completed flows, pending batch size: %d", len(completedFlowIDs), len(pendingBatch))

		case <-batchTimer.C:
			if len(pendingBatch) > 0 {
				if err := anchorFlowBatch(client, pendingBatch, privKey, writerID, genesisHash, verifiedState); err != nil {
					log.Printf("[timeout] anchor batch failed: %v", err)
					failedFlows := make([]string, len(pendingBatch))
					for i, vf := range pendingBatch {
						failedFlows[i] = vf.FlowID
					}
					verifiedState.UnmarkAnchored(failedFlows)
				} else {
					flowIDs := make([]string, len(pendingBatch))
					for i, vf := range pendingBatch {
						flowIDs[i] = vf.FlowID
					}
					log.Printf("[timeout] anchored partial batch of %d flows: %v", len(pendingBatch), flowIDs)
				}
				pendingBatch = nil
			}
			batchTimer.Reset(batchTimeout)
			// log.Printf("[poll] batch timeout: pending batch size: %d", len(pendingBatch))

		}
	}
}
