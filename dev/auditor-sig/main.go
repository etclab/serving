// Package main provides a standalone auditor that polls completed flow records
// from etcd (written by the audit-sink service), verifies BGLS aggregate
// signatures, and anchors verified batches onto the global audit chain.
//
// The auditor runs outside the cluster (no SGX) and uses client Ed25519 keys
// from dev/client/ for signing global chain entries. BGLS public keys are
// extracted from attested-publicKey records discovered during global chain
// verification.
//
// Usage:
//
//	go run ./dev/auditor-sig/ \
//	  -etcd-endpoints localhost:2379 \
//	  -keys-dir dev/client
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

	bls "github.com/cloudflare/circl/ecc/bls12381"
	bgls03 "github.com/etclab/ncircl/aggsig/bgls03"
	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/bgls"
	"knative.dev/serving/pkg/kregistry"
)

const (
	completedPrefix = "lambada/completed/"
)

// CompletedFlowRecord is a record written by the audit-sink service.
type CompletedFlowRecord struct {
	Nonce         string `json:"nonce"`
	AggSignature  string `json:"agg_signature"`
	FunctionChain string `json:"function_chain"`
	Timestamp     string `json:"timestamp"`
}

// VerifiedFlow holds the result of verifying a single completed flow's BGLS signature.
type VerifiedFlow struct {
	FlowID    string   // nonce
	FuncNames []string // pod IDs from function chain
}

// ============================================================
// Writer Public Key Cache (no SGX attestation)
// ============================================================

var (
	pubKeyCacheMu sync.RWMutex
	pubKeyCache   = make(map[string]ed25519.PublicKey)
)

// ============================================================
// BGLS Key Caches
// ============================================================

var (
	bglsPubKeysMu   sync.RWMutex
	bglsPubKeys     = make(map[string]*bgls03.PublicKey)
	bglsPubParamsMu sync.RWMutex
	bglsPubParams   = make(map[string]*bgls03.PublicParams)
)

func cacheBGLSKeys(podID string, pp *bgls03.PublicParams, pk *bgls03.PublicKey) {
	bglsPubParamsMu.Lock()
	bglsPubParams[podID] = pp
	bglsPubParamsMu.Unlock()

	bglsPubKeysMu.Lock()
	bglsPubKeys[podID] = pk
	bglsPubKeysMu.Unlock()
}

func getBGLSPublicKey(podID string) *bgls03.PublicKey {
	bglsPubKeysMu.RLock()
	defer bglsPubKeysMu.RUnlock()
	return bglsPubKeys[podID]
}

func getBGLSPublicParams(podID string) *bgls03.PublicParams {
	bglsPubParamsMu.RLock()
	defer bglsPubParamsMu.RUnlock()
	return bglsPubParams[podID]
}

// ============================================================
// Global Chain Verified State (shared between watcher and anchor)
// ============================================================

// VerifiedState tracks the last verified state of the global chain.
type VerifiedState struct {
	mu             sync.RWMutex
	idx            uint64
	digest         []byte
	head           *kregistry.HashChainHead
	headModRev     int64
	anchoredFlows  map[string]bool
	lastScannedRev int64
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
// Writer Public Key (no SGX attestation)
// ============================================================

func getWriterPublicKeyNoAttestation(client *clientv3.Client, writerID string) (ed25519.PublicKey, error) {
	pubKeyCacheMu.RLock()
	if cached, ok := pubKeyCache[writerID]; ok {
		pubKeyCacheMu.RUnlock()
		return cached, nil
	}
	pubKeyCacheMu.RUnlock()

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

	// Also extract and cache BGLS keys if present
	if attestedKey.SignatureEnabled && len(attestedKey.SigPpBytes) > 0 && len(attestedKey.SigPkBytes) > 0 {
		extractAndCacheBGLSKeys(writerID, &attestedKey)
	}

	pubKeyCacheMu.Lock()
	pubKeyCache[writerID] = pubKey
	pubKeyCacheMu.Unlock()

	return pubKey, nil
}

// extractAndCacheBGLSKeys deserializes BGLS keys from an AttestedPublicKey and caches them.
func extractAndCacheBGLSKeys(podID string, attestedKey *kregistry.AttestedPublicKey) {
	var pps bgls.PublicParamsSerialized
	if err := json.Unmarshal(attestedKey.SigPpBytes, &pps); err != nil {
		log.Printf("[bgls] failed to unmarshal BGLS public params for %s: %v", podID, err)
		return
	}
	sigPp, err := pps.DeSerialize()
	if err != nil {
		log.Printf("[bgls] failed to deserialize BGLS public params for %s: %v", podID, err)
		return
	}

	var pks bgls.PublicKeySerialized
	if err := json.Unmarshal(attestedKey.SigPkBytes, &pks); err != nil {
		log.Printf("[bgls] failed to unmarshal BGLS public key for %s: %v", podID, err)
		return
	}
	sigPk, err := pks.DeSerialize()
	if err != nil {
		log.Printf("[bgls] failed to deserialize BGLS public key for %s: %v", podID, err)
		return
	}

	cacheBGLSKeys(podID, sigPp, sigPk)
	log.Printf("[bgls] cached BGLS keys for pod %s", podID)
}

// ============================================================
// Completed Flow Scanner
// ============================================================

// scanCompletedFlows polls etcd for completed flow records written by audit-sink. ok
// Uses WithMinModRev for efficient incremental scanning.
func scanCompletedFlows(client *clientv3.Client, verifiedState *VerifiedState) ([]CompletedFlowRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lastScannedRev := verifiedState.GetLastScannedRev()

	opts := []clientv3.OpOption{
		clientv3.WithPrefix(),
	}
	if lastScannedRev > 0 {
		opts = append(opts, clientv3.WithMinModRev(lastScannedRev+1))
	}

	resp, err := client.Get(ctx, completedPrefix, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to list completed flows: %w", err)
	}

	var maxRevSeen int64
	var records []CompletedFlowRecord

	for _, kv := range resp.Kvs {
		if kv.ModRevision > maxRevSeen {
			maxRevSeen = kv.ModRevision
		}

		// Extract nonce from key
		nonce := strings.TrimPrefix(string(kv.Key), completedPrefix)
		if nonce == "" {
			continue
		}

		if verifiedState.IsAnchored(nonce) {
			continue
		}

		var rec CompletedFlowRecord
		if err := json.Unmarshal(kv.Value, &rec); err != nil {
			log.Printf("[scan] failed to unmarshal completed record for %s: %v", nonce, err)
			continue
		}

		records = append(records, rec)
	}

	if maxRevSeen > 0 {
		verifiedState.UpdateLastScannedRev(maxRevSeen)
	}

	return records, nil
}

// ============================================================
// BGLS Aggregate Signature Verification
// ============================================================

// verifyBGLSSignature verifies the BGLS aggregate signature on a completed flow record.
// Returns nil on success, an error describing the failure otherwise.
// Returns a special "missing keys" error if BGLS keys for some pods haven't been cached yet.
func verifyBGLSSignature(record CompletedFlowRecord) error {
	if record.AggSignature == "" || record.FunctionChain == "" {
		return fmt.Errorf("empty signature or function chain for nonce %s", record.Nonce)
	}

	// 1. Decode aggregate signature: hex string -> bytes -> bls.G1 -> bgls03.Signature
	sigBytes, err := hex.DecodeString(record.AggSignature)
	if err != nil {
		return fmt.Errorf("failed to hex-decode aggregate signature: %w", err)
	}

	g1 := new(bls.G1)
	if err := g1.SetBytes(sigBytes); err != nil {
		return fmt.Errorf("failed to parse signature as bls.G1: %w", err)
	}

	aggSig := &bgls03.Signature{Sig: g1}

	// 2. Split function chain to get pod IDs
	chain := strings.Split(record.FunctionChain, "|")

	// 3. Build messages and collect public keys
	msgs := make([][]byte, 0, len(chain))
	pks := make([]*bgls03.PublicKey, 0, len(chain))

	var pp *bgls03.PublicParams

	for _, podID := range chain {
		pk := getBGLSPublicKey(podID)
		if pk == nil {
			return fmt.Errorf("BGLS public key not found for pod %s (missing keys)", podID)
		}

		if pp == nil {
			pp = getBGLSPublicParams(podID)
			if pp == nil {
				return fmt.Errorf("BGLS public params not found for pod %s (missing keys)", podID)
			}
		}

		msgs = append(msgs, []byte(record.Nonce+"|"+podID))
		pks = append(pks, pk)
	}

	// 4. Verify aggregate signature
	if err := bgls03.Verify(pp, pks, msgs, aggSig); err != nil {
		return fmt.Errorf("BGLS verification failed for nonce %s: %w", record.Nonce, err)
	}

	return nil
}

// ============================================================
// Global Chain Verification & Anchoring
// (Reused from dev/auditor/main.go with BGLS key extraction)
// ============================================================

func verifyGlobalChain(client *clientv3.Client, startIdx uint64, startDigest []byte) (
	head *kregistry.HashChainHead, headModRev int64, verifiedIdx uint64, verifiedDigest []byte, anchoredFlows []string, err error,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var discoveredFlows []string

	headResp, err := client.Get(ctx, kregistry.HashChainHeadKey)
	if err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to get global chain head: %w", err)
	}
	if len(headResp.Kvs) == 0 {
		log.Printf("[verifyGlobalChain] no global chain head found, starting fresh")
		return nil, 0, 0, startDigest, nil, nil
	}

	var h kregistry.HashChainHead
	if err := json.Unmarshal(headResp.Kvs[0].Value, &h); err != nil {
		return nil, 0, 0, nil, nil, fmt.Errorf("failed to unmarshal global chain head: %w", err)
	}
	headModRev = headResp.Kvs[0].ModRevision

	if h.Idx < startIdx {
		return &h, headModRev, startIdx - 1, startDigest, nil, nil
	}

	// Batch-fetch entries
	// TODO: fetch 128 entries at a time - not more than that
	entryStartKey := kregistry.FormatEntryKey(startIdx)
	entryEndKey := kregistry.FormatEntryKey(h.Idx + 1)

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
		// TODO: here too, ensure keys don't exceed 128 while fetching
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

	// Verify each entry and extract anchored flows + BGLS keys
	prevDigest := startDigest
	for _, entry := range entries {
		if !bytes.Equal(entry.PrevDigest, prevDigest) {
			return nil, 0, 0, nil, nil, fmt.Errorf("chain broken at idx %d: prev_digest mismatch", entry.Idx)
		}

		writerPubKey, err := getWriterPublicKeyNoAttestation(client, entry.WriterID)
		if err != nil {
			return nil, 0, 0, nil, nil, fmt.Errorf("failed to get writer key for %s at idx %d: %w", entry.WriterID, entry.Idx, err)
		}

		entrySignMsg := kregistry.ComputeEntrySignatureMessage(entry)
		if !ed25519.Verify(writerPubKey, entrySignMsg, entry.EntrySig) {
			return nil, 0, 0, nil, nil, fmt.Errorf("entry signature verification failed at idx %d", entry.Idx)
		}

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

		// Extract BGLS keys from PUT entries for attested public keys
		if entry.OpType == "PUT" && strings.Contains(entry.DataKey, kregistry.EnclavePublicKeySuffix) {
			var attestedKey kregistry.AttestedPublicKey
			if err := json.Unmarshal(dataRecord.Payload, &attestedKey); err == nil {
				if attestedKey.SignatureEnabled && len(attestedKey.SigPpBytes) > 0 && len(attestedKey.SigPkBytes) > 0 {
					extractAndCacheBGLSKeys(entry.WriterID, &attestedKey)
				}
			}
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

		prevDigest = kregistry.ComputeNewDigest(prevDigest, entrySignMsg, entry)

		log.Printf("[verifyGlobalChain] verified entry idx %d (op=%s)", entry.Idx, entry.OpType)
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

// ============================================================
// Flow Anchoring (CAS to global chain)
// ============================================================

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

	verifiedIdx, verifiedDigest, head, headModRev := verifiedState.Get()

	var idx uint64
	var prevDigest []byte
	isFirstWrite := head == nil

	if isFirstWrite {
		idx = 1
		prevDigest = genesisHash
		if len(prevDigest) == 0 {
			prevDigest = make([]byte, 32)
		}
	} else {
		idx = verifiedIdx + 1
		prevDigest = verifiedDigest
	}

	// Build anchor payload
	anchorEntries := make([]kregistry.FlowAnchorEntry, len(batch))
	for i, vf := range batch {
		anchorEntries[i] = kregistry.FlowAnchorEntry{
			FlowID:    vf.FlowID,
			Functions: vf.FuncNames,
		}
	}

	// TODO: anchored flow payloads need signatures too
	payload := kregistry.FlowAnchorPayload{
		AnchoredFlows: anchorEntries,
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal anchor payload: %w", err)
	}

	// Build data record
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

	// Build entry record
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

	newDigest := kregistry.ComputeNewDigest(prevDigest, entrySignMsg, entry)

	headSignMsg := kregistry.ComputeHeadSignatureMessage(idx, writerID, newDigest)
	headSig := ed25519.Sign(signingKey, headSignMsg)

	newHead := &kregistry.HashChainHead{
		Idx:      idx,
		Digest:   newDigest,
		WriterID: writerID,
		HeadSig:  headSig,
	}

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

	// CAS transaction
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

	head, headModRev, verifiedIdx, verifiedDigest, anchoredFlows, err := verifyGlobalChain(client, 1, genesisHash)
	if err != nil {
		log.Printf("[watcher] initial verification failed: %v", err)
	} else {
		log.Printf("[watcher] initially verified up to idx=%d", verifiedIdx)
		verifiedState.Update(verifiedIdx, verifiedDigest, head, headModRev)
		if len(anchoredFlows) > 0 {
			verifiedState.MarkAnchored(anchoredFlows)
			log.Printf("[watcher] discovered %d previously anchored flows", len(anchoredFlows))
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
		pollInterval  time.Duration
		batchSize     int
		batchTimeout  time.Duration
		writerID      string
	)

	flag.StringVar(&etcdEndpoints, "etcd-endpoints", "localhost:2379", "Comma-separated etcd endpoints")
	flag.StringVar(&keysDir, "keys-dir", "dev/client", "Directory containing client-pk.pem, client-sk.pem, genesis.hash")
	flag.DurationVar(&pollInterval, "poll-interval", 200*time.Millisecond, "Interval between completed flow scans")
	flag.IntVar(&batchSize, "batch-size", 5, "Number of verified flows per anchor batch")
	flag.DurationVar(&batchTimeout, "batch-timeout", 2*time.Second, "Commit partial batch after this timeout")
	flag.StringVar(&writerID, "writer-id", "auditor", "Writer ID for global chain entries")
	flag.Parse()

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

	// Register auditor's public key
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

	verifiedState := &VerifiedState{
		anchoredFlows: make(map[string]bool),
	}

	// Start global chain watcher (also extracts BGLS keys from attested-publicKey entries)
	go watchGlobalChain(client, genesisHash, verifiedState)

	// Give the watcher a moment to do initial verification and cache BGLS keys
	time.Sleep(2 * time.Second)

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
			records, err := scanCompletedFlows(client, verifiedState)
			if err != nil {
				log.Printf("[poll] scan error: %v", err)
				continue
			}

			for _, rec := range records {
				if err := verifyBGLSSignature(rec); err != nil {
					if strings.Contains(err.Error(), "missing keys") {
						// BGLS keys not yet cached — skip for retry on next poll
						// this shouldn't happen - at least for now
						log.Printf("[poll] skipping nonce %s: %v", rec.Nonce, err)
					} else {
						log.Printf("[poll] BGLS verification FAILED for nonce %s: %v", rec.Nonce, err)
					}
					continue
				}

				chain := strings.Split(rec.FunctionChain, "|")
				log.Printf("[poll] verified BGLS signature for nonce %s (chain=%v)", rec.Nonce, chain)

				pendingBatch = append(pendingBatch, VerifiedFlow{
					FlowID:    rec.Nonce,
					FuncNames: chain,
				})
				verifiedState.MarkAnchored([]string{rec.Nonce})
			}

			if len(pendingBatch) >= batchSize {
				commitBatch := pendingBatch[:batchSize]
				if err := anchorFlowBatch(client, commitBatch, privKey, writerID, genesisHash, verifiedState); err != nil {
					log.Printf("[poll] anchor batch failed: %v", err)
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
		}
	}
}
