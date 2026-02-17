// Package main provides a lightweight in-cluster CloudEvent sink that receives
// completed function chain events and writes completion records to etcd.
//
// The sink extracts Ce-Nonce, Ce-Aggsignature, and Ce-Functionchain headers
// from incoming CloudEvents and batches writes to etcd for efficiency.
// No cryptographic verification is performed here — that is the job of the
// external auditor-sig process.
//
// Usage (in-cluster):
//
//	ETCD_ENDPOINTS=etcd.knative-serving.svc.cluster.local:2379 ./audit-sink
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

const (
	completedPrefix = "lambada/completed/"
)

// TODO: might need to store intermediate signatures for each functions in the chain
// CompletedRecord is the record stored in etcd for each completed flow.
type CompletedRecord struct {
	Nonce         string `json:"nonce"`
	AggSignature  string `json:"agg_signature"`
	FunctionChain string `json:"function_chain"`
	Timestamp     string `json:"timestamp"`
}

// batcher accumulates records and flushes them to etcd in batches.
type batcher struct {
	mu        sync.Mutex
	buffer    []CompletedRecord
	client    *clientv3.Client
	batchSize int
	flushCh   chan struct{} // signals that buffer may be ready to flush
}

func newBatcher(client *clientv3.Client, batchSize int) *batcher {
	return &batcher{
		client:    client,
		batchSize: batchSize,
		flushCh:   make(chan struct{}, 1),
	}
}

// add appends a record to the buffer and signals the flusher if the batch is full.
func (b *batcher) add(rec CompletedRecord) {
	b.mu.Lock()
	b.buffer = append(b.buffer, rec)
	shouldSignal := len(b.buffer) >= b.batchSize
	b.mu.Unlock()

	if shouldSignal {
		select {
		case b.flushCh <- struct{}{}:
		default:
		}
	}
}

// drain removes up to batchSize records from the buffer and returns them.
func (b *batcher) drain() []CompletedRecord {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.buffer) == 0 {
		return nil
	}

	n := min(b.batchSize, len(b.buffer))

	batch := make([]CompletedRecord, n)
	copy(batch, b.buffer[:n])
	b.buffer = b.buffer[n:]
	return batch
}

// flush writes a batch of records to etcd in a single transaction.
func (b *batcher) flush(batch []CompletedRecord) error {
	if len(batch) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// TODO: ensure batch size <= 128
	ops := make([]clientv3.Op, len(batch))
	for i, rec := range batch {
		// using separate keys here is fine as long as we commit multiple keys
		// in a single transaction
		key := completedPrefix + rec.Nonce
		val, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("failed to marshal record for nonce %s: %w", rec.Nonce, err)
		}
		ops[i] = clientv3.OpPut(key, string(val))
	}

	_, err := b.client.Txn(ctx).Then(ops...).Commit()
	if err != nil {
		return fmt.Errorf("etcd transaction failed: %w", err)
	}

	return nil
}

// runFlusher runs in a background goroutine, flushing batches on size or timeout triggers.
func (b *batcher) runFlusher(flushInterval time.Duration) {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-b.flushCh:
			for {
				batch := b.drain()
				if batch == nil {
					break
				}
				if err := b.flush(batch); err != nil {
					log.Printf("[flush] error writing batch: %v", err)
				} else {
					nonces := make([]string, len(batch))
					for i, r := range batch {
						nonces[i] = r.Nonce
					}
					log.Printf("[flush] wrote %d records: %v", len(batch), nonces)
				}
			}
		case <-ticker.C:
			batch := b.drain()
			if batch != nil {
				if err := b.flush(batch); err != nil {
					log.Printf("[flush] timeout flush error: %v", err)
				} else {
					nonces := make([]string, len(batch))
					for i, r := range batch {
						nonces[i] = r.Nonce
					}
					log.Printf("[flush] wrote %d records: %v", len(batch), nonces)
				}
			}
		}
	}
}

func main() {
	// Configuration from environment variables
	etcdEndpoints := envOrDefault("ETCD_ENDPOINTS", "etcd.knative-serving.svc.cluster.local:2379")
	batchSize := envIntOrDefault("BATCH_SIZE", 50)
	flushInterval := envDurationOrDefault("FLUSH_INTERVAL", 500*time.Millisecond)
	port := envOrDefault("PORT", "8080")

	log.Printf("[main] starting audit-sink: etcd=%s batchSize=%d flushInterval=%v port=%s",
		etcdEndpoints, batchSize, flushInterval, port)

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

	b := newBatcher(client, batchSize)
	go b.runFlusher(flushInterval)

	// Health check endpoint for readiness/liveness probes
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// TODO: verify if this going to work
	// the messages are sent via cloud events
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		nonce := r.Header.Get("Ce-Nonce")
		aggSig := r.Header.Get("Ce-Aggsignature")
		funcChain := r.Header.Get("Ce-Functionchain")

		if nonce == "" {
			http.Error(w, "missing Ce-Nonce header", http.StatusBadRequest)
			return
		}

		rec := CompletedRecord{
			Nonce:         nonce,
			AggSignature:  aggSig,
			FunctionChain: funcChain,
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		}

		b.add(rec)

		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "buffered nonce=%s\n", nonce)
	})

	log.Printf("[main] listening on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return def
}

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			return d
		}
	}
	return def
}
