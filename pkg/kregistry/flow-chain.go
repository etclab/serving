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

// FlowChainResult holds the result of an async flow chain recording.
type FlowChainResult struct {
	FlowIdx uint64
	Err     error
}

// flowChainResultContextKey is the context key for storing the flow chain result channel.
type flowChainResultContextKey struct{}

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

	headKey := flowHeadKey(flowID)

	// 1. Fetch head
	headResp, err := kr.Client().Get(ctx, headKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get flow chain head: %w", err)
	}
	if len(headResp.Kvs) == 0 {
		return nil, fmt.Errorf("flow chain head not found for flow %s", flowID)
	}

	var head FlowHeadRecord
	if err := json.Unmarshal(headResp.Kvs[0].Value, &head); err != nil {
		return nil, fmt.Errorf("failed to unmarshal flow chain head: %w", err)
	}
	headModRev := headResp.Kvs[0].ModRevision

	logDev("Flow chain head: flowID=%s, idx=%d", flowID, head.Idx)

	// 2. Batch-fetch all entries 0..head.Idx via range query
	startKey := flowEntryKey(flowID, 0)
	endKey := flowEntryKey(flowID, head.Idx+1)

	entryResp, err := kr.Client().Get(ctx, startKey, clientv3.WithRange(endKey))
	if err != nil {
		return nil, fmt.Errorf("failed to fetch flow chain entries: %w", err)
	}

	entries := make([]*FlowEntryRecord, 0, len(entryResp.Kvs))
	for _, kv := range entryResp.Kvs {
		var entry FlowEntryRecord
		if err := json.Unmarshal(kv.Value, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal flow entry: %w", err)
		}
		entries = append(entries, &entry)
	}

	// Sort by index
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Idx < entries[j].Idx
	})

	// 3. Batch-fetch all referenced data records
	dataKeys := make([]string, len(entries))
	for i, entry := range entries {
		dataKeys[i] = entry.DataKey
	}

	dataRecords := make(map[string]*FlowDataRecord, len(entries))
	if len(dataKeys) > 0 {
		ops := make([]clientv3.Op, len(dataKeys))
		for i, key := range dataKeys {
			ops[i] = clientv3.OpGet(key)
		}

		txnResp, err := kr.Client().Txn(ctx).Then(ops...).Commit()
		if err != nil {
			return nil, fmt.Errorf("failed to batch fetch flow data records: %w", err)
		}

		for i, key := range dataKeys {
			rangeResp := txnResp.Responses[i].GetResponseRange()
			if len(rangeResp.Kvs) == 0 {
				return nil, fmt.Errorf("flow data not found at %s", key)
			}
			var record FlowDataRecord
			if err := json.Unmarshal(rangeResp.Kvs[0].Value, &record); err != nil {
				return nil, fmt.Errorf("failed to unmarshal flow data record at %s: %w", key, err)
			}
			dataRecords[record.FuncName] = &record
		}
	}

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
// It verifies the existing chain, checks position ordering, then appends.
func (kr *KeyRegistry) WriteFlowChainSubsequent(ctx context.Context, flowID string, genesisHash []byte) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - WriteFlowChainSubsequent")

	if kr.EnclavePrivateKey == nil {
		return 0, fmt.Errorf("enclave private key is nil")
	}

	// 1. Full chain verification
	// TODO: we don't need to verify the entire chain here as it was already verified
	// TODO: before we started processing. save the result somewhere to avoid repeating it
	result, err := kr.VerifyFlowChain(ctx, flowID, genesisHash)
	if err != nil {
		return 0, fmt.Errorf("flow chain verification failed: %w", err)
	}

	// 2. Position verification
	chainedServices := kr.GetFunctionChainFromEnv()
	err = kr.VerifyFlowChainPosition(result.Entries, chainedServices, kr.ServiceName)
	if err != nil {
		return 0, fmt.Errorf("flow chain position verification failed: %w", err)
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
func (kr *KeyRegistry) RecordFlowOnChain(flowID string) (uint64, error) {
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

	maxAttempts := 20
	backoff := 100 * time.Millisecond
	maxBackoff := 5 * time.Second

	for attempt := 0; attempt < maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)

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

// StartFlowChainRecordingAsync starts recording flow processing on the per-flow chain
// in a background goroutine. Returns a context with the result channel embedded,
// and the channel itself.
func (kr *KeyRegistry) StartFlowChainRecordingAsync(ctx context.Context, flowID string) (context.Context, <-chan FlowChainResult) {
	logDev := mutil.LogWithPrefix("dev - StartFlowChainRecordingAsync")

	resultChan := make(chan FlowChainResult, 1)

	newCtx := context.WithValue(ctx, flowChainResultContextKey{}, resultChan)

	go func() {
		defer close(resultChan)

		logDev("Starting async flow chain recording: flowID=%s", flowID)
		idx, err := kr.RecordFlowOnChain(flowID)

		result := FlowChainResult{
			FlowIdx: idx,
			Err:     err,
		}

		logDev("Async flow chain recording complete: flowID=%s, idx=%d, err=%v", flowID, idx, err)
		resultChan <- result
	}()

	return newCtx, resultChan
}

// GetFlowChainResultFromContext retrieves the flow chain result channel from the context.
func GetFlowChainResultFromContext(ctx context.Context) <-chan FlowChainResult {
	if ctx == nil {
		return nil
	}
	ch, ok := ctx.Value(flowChainResultContextKey{}).(chan FlowChainResult)
	if !ok {
		return nil
	}
	return ch
}

// WaitForFlowChainResult waits for the async flow chain recording to complete.
func WaitForFlowChainResult(ctx context.Context, timeout time.Duration) (uint64, error) {
	logDev := mutil.LogWithPrefix("dev - WaitForFlowChainResult")

	ch := GetFlowChainResultFromContext(ctx)
	if ch == nil {
		logDev("No flow chain result channel in context, flow tracking not enabled")
		return 0, nil
	}

	select {
	case result, ok := <-ch:
		if !ok {
			logDev("Flow chain result channel closed without result")
			return 0, fmt.Errorf("flow chain result channel closed unexpectedly")
		}
		logDev("Got flow chain result: idx=%d, err=%v", result.FlowIdx, result.Err)
		return result.FlowIdx, result.Err
	case <-time.After(timeout):
		logDev("Timeout waiting for flow chain result after %v", timeout)
		return 0, fmt.Errorf("timeout waiting for flow chain recording to complete")
	case <-ctx.Done():
		logDev("Context cancelled while waiting for flow chain result")
		return 0, ctx.Err()
	}
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
