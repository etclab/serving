package kregistry

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
	"knative.dev/serving/pkg/mutil"
)

// ============================================================
// Per-Flow Hash Chain Data Structures
// ============================================================

const FlowChainPrefix = "lambada/flow/"

// FlowHeadRecord represents the head of a per-flow chain.
type FlowHeadRecord struct {
	FlowID  string `json:"flow_id"`
	Idx     uint64 `json:"idx"`
	Digest  []byte `json:"digest"`
	HeadSig []byte `json:"head_sig"`
}

// FlowEntryRecord represents an entry in a per-flow chain.
type FlowEntryRecord struct {
	FlowID     string `json:"flow_id"`
	Idx        uint64 `json:"idx"`
	PrevDigest []byte `json:"prev_digest"`
	FuncName   string `json:"func_name"`
	DataKey    string `json:"data_key"`
	WriterID   string `json:"writer_id"`
	EntrySig   []byte `json:"entry_sig"`
}

// FlowDataRecord represents a data record in a per-flow chain.
type FlowDataRecord struct {
	FlowID   string `json:"flow_id"`
	Idx      uint64 `json:"idx"`
	FuncName string `json:"func_name"`
	WriterID string `json:"writer_id"`
	DataSig  []byte `json:"data_sig"`
}

// FlowChainVerifyResult holds the result of verifying a per-flow chain.
type FlowChainVerifyResult struct {
	Head           *FlowHeadRecord
	HeadModRev     int64
	VerifiedIdx    uint64
	VerifiedDigest []byte
	Entries        []*FlowEntryRecord
	DataRecords    map[string]*FlowDataRecord // keyed by funcName
}

// FlowChainVerifyResultContextKey is the context key for storing the cached verification result.
type FlowChainVerifyResultContextKey struct{}

// ============================================================
// Key Formatting Functions
// ============================================================

// flowHeadKey returns "lambada/flow/<flow_id>/head"
func flowHeadKey(flowID string) string {
	return FlowChainPrefix + flowID + "/head"
}

// flowEntryKey returns "lambada/flow/<flow_id>/entry/0000" (zero-padded 4 digits)
func flowEntryKey(flowID string, idx uint64) string {
	return fmt.Sprintf("%s%s/entry/%04d", FlowChainPrefix, flowID, idx)
}

// flowDataKey returns "lambada/flow/<flow_id>/data/<func_name>"
func flowDataKey(flowID, funcName string) string {
	return FlowChainPrefix + flowID + "/data/" + funcName
}

// ============================================================
// Signature/Digest Functions (domain-separated from global chain)
// ============================================================

// computeFlowHeadSignatureMessage creates the message to sign for a flow head record.
// Format: H("flow_head" || flowID || idx || digest)
func computeFlowHeadSignatureMessage(flowID string, idx uint64, digest []byte) []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("flow_head")...)
	buf = append(buf, []byte(flowID)...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, digest...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeFlowEntrySignatureMessage creates the message to sign for a flow entry record.
// Format: H("flow_entry" || flowID || idx || prev_digest || func_name || data_key || writer_id)
func computeFlowEntrySignatureMessage(entry *FlowEntryRecord) []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("flow_entry")...)
	buf = append(buf, []byte(entry.FlowID)...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, entry.Idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, entry.PrevDigest...)
	buf = append(buf, []byte(entry.FuncName)...)
	buf = append(buf, []byte(entry.DataKey)...)
	buf = append(buf, []byte(entry.WriterID)...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeFlowDataSignatureMessage creates the message to sign for a flow data record.
// Format: H("flow_data" || flowID || idx || func_name || writer_id)
func computeFlowDataSignatureMessage(record *FlowDataRecord) []byte {
	buf := make([]byte, 0, 256)
	buf = append(buf, []byte("flow_data")...)
	buf = append(buf, []byte(record.FlowID)...)
	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, record.Idx)
	buf = append(buf, idxBytes...)
	buf = append(buf, []byte(record.FuncName)...)
	buf = append(buf, []byte(record.WriterID)...)
	hash := sha256.Sum256(buf)
	return hash[:]
}

// computeFlowDigest computes the chain digest: H(prevDigest || H(entry_fields) || H(entry_sig))
func computeFlowDigest(prevDigest []byte, entryFieldsHash []byte, entry *FlowEntryRecord) []byte {
	sigHash := sha256.Sum256(entry.EntrySig)

	buf := make([]byte, 0, len(prevDigest)+32+32)
	buf = append(buf, prevDigest...)
	buf = append(buf, entryFieldsHash...)
	buf = append(buf, sigHash[:]...)

	hash := sha256.Sum256(buf)
	return hash[:]
}

// ============================================================
// Exported Wrappers for External Verifiers (e.g., auditor)
// ============================================================

// FlowHeadKey returns the etcd key for a flow's head record.
func FlowHeadKey(flowID string) string { return flowHeadKey(flowID) }

// FlowEntryKey returns the etcd key for a flow entry at the given index.
func FlowEntryKey(flowID string, idx uint64) string { return flowEntryKey(flowID, idx) }

// FlowDataKey returns the etcd key for a flow's data record for a given function.
func FlowDataKey(flowID, funcName string) string { return flowDataKey(flowID, funcName) }

// ComputeFlowHeadSignatureMessage creates the message to sign for a flow head record.
func ComputeFlowHeadSignatureMessage(flowID string, idx uint64, digest []byte) []byte {
	return computeFlowHeadSignatureMessage(flowID, idx, digest)
}

// ComputeFlowEntrySignatureMessage creates the message to sign for a flow entry record.
func ComputeFlowEntrySignatureMessage(entry *FlowEntryRecord) []byte {
	return computeFlowEntrySignatureMessage(entry)
}

// ComputeFlowDataSignatureMessage creates the message to sign for a flow data record.
func ComputeFlowDataSignatureMessage(record *FlowDataRecord) []byte {
	return computeFlowDataSignatureMessage(record)
}

// ComputeFlowDigest computes the chain digest for a flow entry.
func ComputeFlowDigest(prevDigest []byte, entryFieldsHash []byte, entry *FlowEntryRecord) []byte {
	return computeFlowDigest(prevDigest, entryFieldsHash, entry)
}

// ============================================================
// Write Path - First Function (Position 0)
// ============================================================

// WriteFlowChainFirst writes the first entry (idx=0) in a per-flow chain.
// This is called by the first function in the chain.
// Returns the index written (0) or an error.
func (kr *KeyRegistry) WriteFlowChainFirst(ctx context.Context, flowID string, genesisHash []byte) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - WriteFlowChainFirst")

	if kr.EnclavePrivateKey == nil {
		return 0, fmt.Errorf("enclave private key is nil")
	}

	idx := uint64(0)
	writerID := kr.PodId
	funcName := kr.ServiceName
	signingKey := kr.EnclavePrivateKey

	// Use genesis hash as prevDigest for the first entry
	prevDigest := genesisHash
	if len(prevDigest) == 0 {
		prevDigest = make([]byte, 32)
	}

	headKey := flowHeadKey(flowID)
	entryKey := flowEntryKey(flowID, idx)
	dataKeyStr := flowDataKey(flowID, funcName)

	// Build FlowDataRecord
	dataRecord := &FlowDataRecord{
		FlowID:   flowID,
		Idx:      idx,
		FuncName: funcName,
		WriterID: writerID,
	}
	dataSignMsg := computeFlowDataSignatureMessage(dataRecord)
	dataRecord.DataSig = ed25519.Sign(signingKey, dataSignMsg)

	// Build FlowEntryRecord
	entry := &FlowEntryRecord{
		FlowID:     flowID,
		Idx:        idx,
		PrevDigest: prevDigest,
		FuncName:   funcName,
		DataKey:    dataKeyStr,
		WriterID:   writerID,
	}
	entrySignMsg := computeFlowEntrySignatureMessage(entry)
	entry.EntrySig = ed25519.Sign(signingKey, entrySignMsg)

	// Compute digest
	newDigest := computeFlowDigest(prevDigest, entrySignMsg, entry)

	// Build FlowHeadRecord
	headRecord := &FlowHeadRecord{
		FlowID: flowID,
		Idx:    idx,
		Digest: newDigest,
	}
	headSignMsg := computeFlowHeadSignatureMessage(flowID, idx, newDigest)
	headRecord.HeadSig = ed25519.Sign(signingKey, headSignMsg)

	// Marshal records
	dataBytes, err := json.Marshal(dataRecord)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow data record: %w", err)
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow entry record: %w", err)
	}
	headBytes, err := json.Marshal(headRecord)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow head record: %w", err)
	}

	// CAS transaction: all three keys must not exist
	txnResp, err := kr.Client().Txn(ctx).If(
		clientv3.Compare(clientv3.CreateRevision(headKey), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(dataKeyStr), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),
	).Then(
		clientv3.OpPut(dataKeyStr, string(dataBytes)),
		clientv3.OpPut(entryKey, string(entryBytes)),
		clientv3.OpPut(headKey, string(headBytes)),
	).Commit()
	if err != nil {
		return 0, fmt.Errorf("flow chain first write transaction failed: %w", err)
	}

	if !txnResp.Succeeded {
		// Head already exists => replay or concurrent start
		return 0, fmt.Errorf("flow already started: flow %s head already exists", flowID)
	}

	logDev("Successfully wrote first flow chain entry: flowID=%s, idx=%d", flowID, idx)
	return idx, nil
}

// ============================================================
// Read/Verify Path
// ============================================================

// VerifyFlowChain verifies the entire per-flow chain for a given flowID.
// Returns the verification result or an error.
func (kr *KeyRegistry) VerifyFlowChain(ctx context.Context, flowID string, genesisHash []byte) (*FlowChainVerifyResult, error) {
	logDev := mutil.LogWithPrefix("dev - VerifyFlowChain")

	// Single prefix query to fetch all flow data (head, entries, data records)
	flowPrefix := FlowChainPrefix + flowID + "/"
	resp, err := kr.Client().Get(ctx, flowPrefix, clientv3.WithPrefix())
	if err != nil {
		return nil, fmt.Errorf("failed to fetch flow chain data: %w", err)
	}

	if len(resp.Kvs) == 0 {
		return nil, fmt.Errorf("flow chain not found for flow %s", flowID)
	}

	// Parse response into head, entries, and data records
	var head FlowHeadRecord
	var headModRev int64
	var headFound bool
	entries := make([]*FlowEntryRecord, 0)
	dataRecords := make(map[string]*FlowDataRecord)

	headKey := flowHeadKey(flowID)
	entryPrefix := FlowChainPrefix + flowID + "/entry/"
	dataPrefix := FlowChainPrefix + flowID + "/data/"

	for _, kv := range resp.Kvs {
		key := string(kv.Key)

		if key == headKey {
			// Parse head record
			if err := json.Unmarshal(kv.Value, &head); err != nil {
				return nil, fmt.Errorf("failed to unmarshal flow chain head: %w", err)
			}
			headModRev = kv.ModRevision
			headFound = true
		} else if strings.HasPrefix(key, entryPrefix) {
			// Parse entry record
			var entry FlowEntryRecord
			if err := json.Unmarshal(kv.Value, &entry); err != nil {
				return nil, fmt.Errorf("failed to unmarshal flow entry: %w", err)
			}
			entries = append(entries, &entry)
		} else if strings.HasPrefix(key, dataPrefix) {
			// Parse data record
			var record FlowDataRecord
			if err := json.Unmarshal(kv.Value, &record); err != nil {
				return nil, fmt.Errorf("failed to unmarshal flow data record: %w", err)
			}
			dataRecords[record.FuncName] = &record
		}
	}

	logDev("Fetched flow chain data: flowID=%s, headFound=%t, entries=%d, dataRecords=%d",
		flowID, headFound, len(entries), len(dataRecords))

	if !headFound {
		return nil, fmt.Errorf("flow chain head not found for flow %s", flowID)
	}

	// Sort entries by index
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Idx < entries[j].Idx
	})

	logDev("Fetched flow chain in single request: flowID=%s, head.idx=%d, entries=%d, dataRecords=%d",
		flowID, head.Idx, len(entries), len(dataRecords))

	// 4. Verify each entry: chain link, entry_sig, data_sig
	prevDigest := genesisHash
	if len(prevDigest) == 0 {
		prevDigest = make([]byte, 32)
	}

	for _, entry := range entries {
		// Get writer's public key (reuses existing attestation verification)
		writerPubKey, err := kr.getWriterPublicKey(ctx, entry.WriterID)
		if err != nil {
			return nil, fmt.Errorf("failed to get writer public key for %s: %w", entry.WriterID, err)
		}

		// Verify entry signature
		entrySignMsg := computeFlowEntrySignatureMessage(entry)
		if !ed25519.Verify(writerPubKey, entrySignMsg, entry.EntrySig) {
			return nil, fmt.Errorf("flow entry signature verification failed at idx %d", entry.Idx)
		}

		// Verify data signature
		dataRecord, ok := dataRecords[entry.FuncName]
		if !ok {
			return nil, fmt.Errorf("flow data record not found for func %s at idx %d", entry.FuncName, entry.Idx)
		}
		dataSignMsg := computeFlowDataSignatureMessage(dataRecord)
		if !ed25519.Verify(writerPubKey, dataSignMsg, dataRecord.DataSig) {
			return nil, fmt.Errorf("flow data signature verification failed for func %s", entry.FuncName)
		}

		// Compute chain digest and advance
		prevDigest = computeFlowDigest(prevDigest, entrySignMsg, entry)

		logDev("Verified flow entry idx=%d, func=%s, writer=%s", entry.Idx, entry.FuncName, entry.WriterID)
	}

	// 5. Verify final digest matches head.Digest
	if len(entries) > 0 {
		if !bytesEqual(prevDigest, head.Digest) {
			return nil, fmt.Errorf("flow chain digest mismatch: expected %x, got %x", head.Digest, prevDigest)
		}
	}

	// 6. Verify head signature
	headWriterPubKey, err := kr.getWriterPublicKey(ctx, entries[len(entries)-1].WriterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get head writer public key: %w", err)
	}
	headSignMsg := computeFlowHeadSignatureMessage(flowID, head.Idx, head.Digest)
	if !ed25519.Verify(headWriterPubKey, headSignMsg, head.HeadSig) {
		return nil, fmt.Errorf("flow chain head signature verification failed")
	}

	logDev("Flow chain verified: flowID=%s, idx=%d", flowID, head.Idx)

	return &FlowChainVerifyResult{
		Head:           &head,
		HeadModRev:     headModRev,
		VerifiedIdx:    head.Idx,
		VerifiedDigest: prevDigest,
		Entries:        entries,
		DataRecords:    dataRecords,
	}, nil
}

// bytesEqual is a helper for comparing byte slices.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// VerifyFlowChainPosition checks that the entries in the flow chain match the expected
// prefix of the function chain for the current service.
func (kr *KeyRegistry) VerifyFlowChainPosition(entries []*FlowEntryRecord, chainedServices []string, currentService string) error {
	position := slices.Index(chainedServices, currentService)
	if position < 0 {
		return fmt.Errorf("current service %s not found in function chain", currentService)
	}

	// For position N, we expect exactly N entries before us (indices 0..N-1)
	if len(entries) != position {
		return fmt.Errorf("expected %d entries before %s (position %d), got %d entries",
			position, currentService, position, len(entries))
	}

	// Verify each entry matches the expected function in order
	for i, entry := range entries {
		expectedFunc := chainedServices[i]
		if entry.FuncName != expectedFunc {
			return fmt.Errorf("flow chain position mismatch at idx %d: expected func %s, got %s",
				i, expectedFunc, entry.FuncName)
		}
	}

	return nil
}

// ============================================================
// Write Path - Subsequent Functions (Position > 0)
// ============================================================

// WriteFlowChainSubsequent writes a subsequent entry in a per-flow chain.
// This is called by functions at position > 0.
// It uses the cached verification result from context if available, otherwise verifies the chain.
func (kr *KeyRegistry) WriteFlowChainSubsequent(ctx context.Context, flowID string, genesisHash []byte) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - WriteFlowChainSubsequent")

	if kr.EnclavePrivateKey == nil {
		return 0, fmt.Errorf("enclave private key is nil")
	}

	// 1. Try to get cached verification result from context
	var result *FlowChainVerifyResult
	if cachedResult := GetFlowChainVerifyResultFromContext(ctx); cachedResult != nil {
		logDev("Using cached verification result for flow %s", flowID)
		result = cachedResult
	} else {
		// Fallback: verify the chain if no cached result (shouldn't happen in normal flow)
		logDev("No cached verification result, verifying flow chain for %s", flowID)
		var err error
		result, err = kr.VerifyFlowChain(ctx, flowID, genesisHash)
		if err != nil {
			return 0, fmt.Errorf("flow chain verification failed: %w", err)
		}

		// Position verification (only needed if we just verified)
		chainedServices := kr.GetFunctionChainFromEnv()
		err = kr.VerifyFlowChainPosition(result.Entries, chainedServices, kr.ServiceName)
		if err != nil {
			return 0, fmt.Errorf("flow chain position verification failed: %w", err)
		}
	}

	// 3. Build new records at idx = verifiedIdx + 1
	idx := result.VerifiedIdx + 1
	writerID := kr.PodId
	funcName := kr.ServiceName
	signingKey := kr.EnclavePrivateKey
	prevDigest := result.VerifiedDigest

	headKey := flowHeadKey(flowID)
	entryKey := flowEntryKey(flowID, idx)
	dataKeyStr := flowDataKey(flowID, funcName)

	// Build FlowDataRecord
	dataRecord := &FlowDataRecord{
		FlowID:   flowID,
		Idx:      idx,
		FuncName: funcName,
		WriterID: writerID,
	}
	dataSignMsg := computeFlowDataSignatureMessage(dataRecord)
	dataRecord.DataSig = ed25519.Sign(signingKey, dataSignMsg)

	// Build FlowEntryRecord
	entry := &FlowEntryRecord{
		FlowID:     flowID,
		Idx:        idx,
		PrevDigest: prevDigest,
		FuncName:   funcName,
		DataKey:    dataKeyStr,
		WriterID:   writerID,
	}
	entrySignMsg := computeFlowEntrySignatureMessage(entry)
	entry.EntrySig = ed25519.Sign(signingKey, entrySignMsg)

	// Compute digest
	newDigest := computeFlowDigest(prevDigest, entrySignMsg, entry)

	// Build FlowHeadRecord
	headRecord := &FlowHeadRecord{
		FlowID: flowID,
		Idx:    idx,
		Digest: newDigest,
	}
	headSignMsg := computeFlowHeadSignatureMessage(flowID, idx, newDigest)
	headRecord.HeadSig = ed25519.Sign(signingKey, headSignMsg)

	// Marshal records
	dataBytes, err := json.Marshal(dataRecord)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow data record: %w", err)
	}
	entryBytes, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow entry record: %w", err)
	}
	headBytes, err := json.Marshal(headRecord)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal flow head record: %w", err)
	}

	// 4. CAS transaction
	txnResp, err := kr.Client().Txn(ctx).If(
		clientv3.Compare(clientv3.ModRevision(headKey), "=", result.HeadModRev),
		// TODO: also check that the head value equal to the verified previous head
		clientv3.Compare(clientv3.CreateRevision(dataKeyStr), "=", 0),
		clientv3.Compare(clientv3.CreateRevision(entryKey), "=", 0),
	).Then(
		clientv3.OpPut(dataKeyStr, string(dataBytes)),
		clientv3.OpPut(entryKey, string(entryBytes)),
		clientv3.OpPut(headKey, string(headBytes)),
	).Commit()
	if err != nil {
		return 0, fmt.Errorf("flow chain subsequent write transaction failed: %w", err)
	}

	if !txnResp.Succeeded {
		return 0, fmt.Errorf("transaction conflict: flow chain head or data key changed")
	}

	logDev("Successfully wrote subsequent flow chain entry: flowID=%s, idx=%d, func=%s", flowID, idx, funcName)
	return idx, nil
}

// ============================================================
// Unified Entry Point with Retry
// ============================================================

// RecordFlowOnChain records this service's processing on the per-flow chain.
// It determines position from FUNCTION_CHAIN env var and calls the appropriate write method.
// Includes retry with exponential backoff for transaction conflicts and head-not-found.
// The baseCtx should contain the cached verification result if available.
func (kr *KeyRegistry) RecordFlowOnChain(baseCtx context.Context, flowID string) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - RecordFlowOnChain")

	if flowID == "" {
		return 0, fmt.Errorf("flowID is empty")
	}
	if kr.ServiceName == "" {
		return 0, fmt.Errorf("service name is not set")
	}

	chainedServices := kr.GetFunctionChainFromEnv()
	position := slices.Index(chainedServices, kr.ServiceName)
	if position < 0 {
		return 0, fmt.Errorf("service %s not found in FUNCTION_CHAIN", kr.ServiceName)
	}

	genesisHash := kr.GenesisHash

	maxAttempts := 5
	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Create timeout context from baseCtx to preserve cached verification result
		ctx, cancel := context.WithTimeout(baseCtx, 10*time.Second)

		var idx uint64
		var err error

		if position == 0 {
			idx, err = kr.WriteFlowChainFirst(ctx, flowID, genesisHash)
		} else {
			idx, err = kr.WriteFlowChainSubsequent(ctx, flowID, genesisHash)
		}
		cancel()

		if err == nil {
			logDev("Successfully recorded flow on chain: flowID=%s, idx=%d, attempt=%d", flowID, idx, attempt+1)
			return idx, nil
		}

		// Replay detected - fail immediately
		if strings.Contains(err.Error(), "flow already started") {
			logDev("Replay detected for flow %s: %v", flowID, err)
			return 0, err
		}

		// Transaction conflict or head not found - retry
		if strings.Contains(err.Error(), "transaction conflict") ||
			strings.Contains(err.Error(), "head not found") ||
			strings.Contains(err.Error(), "flow chain head not found") {
			sleepDuration := addFlowJitter(backoff)
			logDev("Retryable error on attempt %d for flow %s: %v, retrying after %v", attempt+1, flowID, err, sleepDuration)
			time.Sleep(sleepDuration)
			backoff = time.Duration(math.Min(float64(backoff*2), float64(maxBackoff)))
			continue
		}

		// Other errors - fail immediately
		logDev("Non-retryable error on attempt %d for flow %s: %v", attempt+1, flowID, err)
		return 0, err
	}

	return 0, fmt.Errorf("failed to record flow %s on chain after %d attempts", flowID, maxAttempts)
}

// addFlowJitter adds random jitter to a duration.
func addFlowJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int63n(int64(d / 2)))
	return d + jitter
}

// ============================================================
// Async Wrapper
// ============================================================

// GetFlowChainVerifyResultFromContext retrieves the cached verification result from the context.
func GetFlowChainVerifyResultFromContext(ctx context.Context) *FlowChainVerifyResult {
	if ctx == nil {
		return nil
	}
	result, ok := ctx.Value(FlowChainVerifyResultContextKey{}).(*FlowChainVerifyResult)
	if !ok {
		return nil
	}
	return result
}

// GetFunctionChainFromEnvStatic returns the function chain from the FUNCTION_CHAIN env var.
// This is a package-level helper that doesn't need a KeyRegistry.
func GetFunctionChainFromEnvStatic() []string {
	functionChain := os.Getenv("FUNCTION_CHAIN")
	if functionChain == "" {
		return []string{}
	}
	return strings.Split(functionChain, "/")
}

// ============================================================
// Flow Chain Worker Pool
// ============================================================

// FlowChainTask represents a unit of work for the flow chain worker pool.
type FlowChainTask struct {
	Nonce           string
	Position        int
	ChainedServices []string
	Attempts        int // how many times this task has been dequeued
}

const maxFlowChainAttempts = 20

// StartFlowChainWorkers launches n worker goroutines that process flow chain
// tasks (verify + write) from a shared buffered channel. This bounds the number
// of concurrent etcd operations and prevents resource contention under high load.
func (kr *KeyRegistry) StartFlowChainWorkers(n int, bufferSize int) {
	kr.flowChainTasks = make(chan FlowChainTask, bufferSize)
	logDev := mutil.LogWithPrefix("dev - FlowChainWorkerPool")
	for i := 0; i < n; i++ {
		go func(workerID int) {
			for task := range kr.flowChainTasks {
				kr.processFlowChainTask(workerID, task)
				time.Sleep(100 * time.Millisecond)
			}
		}(i)
	}
	logDev("Started %d flow chain workers with buffer size %d", n, bufferSize)
}

// EnqueueFlowChainTask sends a task to the worker pool. Returns false if the
// channel buffer is full (non-blocking to avoid stalling the request path).
func (kr *KeyRegistry) EnqueueFlowChainTask(task FlowChainTask) bool {
	logDev := mutil.LogWithPrefix("dev - FlowChainEnqueue")
	select {
	case kr.flowChainTasks <- task:
		queued := len(kr.flowChainTasks)
		capacity := cap(kr.flowChainTasks)
		logDev("Enqueued flow %s (position %d) — buffer %d/%d", task.Nonce, task.Position, queued, capacity)
		return true
	default:
		queued := len(kr.flowChainTasks)
		capacity := cap(kr.flowChainTasks)
		logDev("BUFFER FULL — dropped flow %s (position %d) — buffer %d/%d", task.Nonce, task.Position, queued, capacity)
		return false
	}
}

func (kr *KeyRegistry) processFlowChainTask(workerID int, task FlowChainTask) {
	logBg := mutil.LogWithPrefix("dev - FlowChainWorker")
	bgCtx := context.Background()
	task.Attempts++

	logBg("Worker %d: processing flow %s (position %d, attempt %d/%d)",
		workerID, task.Nonce, task.Position, task.Attempts, maxFlowChainAttempts)

	if task.Position > 0 {
		result, verifyErr := kr.VerifyFlowChain(bgCtx, task.Nonce, kr.GenesisHash)
		if verifyErr != nil {
			logBg("Worker %d: verification failed for flow %s (attempt %d): %v",
				workerID, task.Nonce, task.Attempts, verifyErr)
			kr.reEnqueueFlowChainTask(workerID, task)
			return
		}
		posErr := kr.VerifyFlowChainPosition(result.Entries, task.ChainedServices, kr.ServiceName)
		if posErr != nil {
			logBg("Worker %d: position verification failed for flow %s (attempt %d): %v",
				workerID, task.Nonce, task.Attempts, posErr)
			kr.reEnqueueFlowChainTask(workerID, task)
			return
		}
		bgCtx = context.WithValue(bgCtx, FlowChainVerifyResultContextKey{}, result)
	}

	idx, err := kr.RecordFlowOnChain(bgCtx, task.Nonce)
	if err != nil {
		logBg("Worker %d: recording failed for flow %s (attempt %d): %v",
			workerID, task.Nonce, task.Attempts, err)
		kr.reEnqueueFlowChainTask(workerID, task)
		return
	}
	logBg("Worker %d: recorded flow %s at index %d (attempt %d) — buffer %d/%d",
		workerID, task.Nonce, idx, task.Attempts, len(kr.flowChainTasks), cap(kr.flowChainTasks))
}

func (kr *KeyRegistry) reEnqueueFlowChainTask(workerID int, task FlowChainTask) {
	logBg := mutil.LogWithPrefix("dev - FlowChainWorker")
	if task.Attempts >= maxFlowChainAttempts {
		logBg("Worker %d: GIVING UP on flow %s after %d attempts, skipping write to preserve ordering",
			workerID, task.Nonce, task.Attempts)
		return
	}
	select {
	case kr.flowChainTasks <- task:
		logBg("Worker %d: re-enqueued flow %s (attempt %d) — buffer %d/%d",
			workerID, task.Nonce, task.Attempts, len(kr.flowChainTasks), cap(kr.flowChainTasks))
	default:
		logBg("Worker %d: BUFFER FULL — dropped re-enqueue for flow %s (attempt %d) — buffer %d/%d",
			workerID, task.Nonce, task.Attempts, len(kr.flowChainTasks), cap(kr.flowChainTasks))
	}
}
