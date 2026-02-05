/*
Copyright 2018 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sharedmain

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"runtime"
	"slices"

	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/kelseyhightower/envconfig"
	"go.opencensus.io/plugin/ochttp"
	"go.uber.org/automaxprocs/maxprocs"
	"go.uber.org/zap"
	"knative.dev/serving/pkg/kregistry"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/queue/certificate"
	"knative.dev/serving/pkg/samba"

	"k8s.io/apimachinery/pkg/types"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"knative.dev/networking/pkg/certificates"
	netstats "knative.dev/networking/pkg/http/stats"
	kubeclient "knative.dev/pkg/client/injection/kube/client"
	pkglogging "knative.dev/pkg/logging"
	"knative.dev/pkg/logging/logkey"
	"knative.dev/pkg/metrics"
	pkgnet "knative.dev/pkg/network"
	"knative.dev/pkg/profiling"
	"knative.dev/pkg/signals"
	"knative.dev/pkg/tracing"
	tracingconfig "knative.dev/pkg/tracing/config"
	"knative.dev/pkg/tracing/propagation/tracecontextb3"
	pkghttp "knative.dev/serving/pkg/http"
	"knative.dev/serving/pkg/logging"
	"knative.dev/serving/pkg/networking"
	"knative.dev/serving/pkg/queue"
	"knative.dev/serving/pkg/queue/readiness"

	"github.com/edgelesssys/ego/enclave"
	bgls03 "github.com/etclab/ncircl/aggsig/bgls03"
	"github.com/etclab/pre"
	injection "knative.dev/pkg/injection"
	"knative.dev/serving/pkg/bgls"
)

const (
	// reportingPeriod is the interval of time between reporting stats by queue proxy.
	reportingPeriod = 1 * time.Second

	// Duration the /wait-for-drain handler should wait before returning.
	// This is to give networking a little bit more time to remove the pod
	// from its configuration and propagate that to all loadbalancers and nodes.
	drainSleepDuration = 30 * time.Second

	// certPath is the path for the server certificate mounted by queue-proxy.
	certPath = queue.CertDirectory + "/" + certificates.CertName

	// keyPath is the path for the server certificate key mounted by queue-proxy.
	keyPath = queue.CertDirectory + "/" + certificates.PrivateKeyName

	// PodInfoAnnotationsPath is an exported path for the annotations file
	// This path is used by QP Options (Extensions).
	PodInfoAnnotationsPath = queue.PodInfoDirectory + "/" + queue.PodInfoAnnotationsFilename

	// QPOptionTokenDirPath is a directory for per audience tokens
	// This path is used by QP Options (Extensions) as <QPOptionTokenDirPath>/<Audience>
	QPOptionTokenDirPath = queue.TokenDirectory
)

type config struct {
	ContainerConcurrency int    `split_words:"true" required:"true"`
	QueueServingPort     string `split_words:"true" required:"true"`
	// AttestedTLSPort                     string `split_words:"true"` // optional
	QueueServingTLSPort                 string `split_words:"true" required:"true"`
	UserPort                            string `split_words:"true" required:"true"`
	RevisionTimeoutSeconds              int    `split_words:"true" required:"true"`
	RevisionResponseStartTimeoutSeconds int    `split_words:"true"` // optional
	RevisionIdleTimeoutSeconds          int    `split_words:"true"` // optional
	ServingReadinessProbe               string `split_words:"true"` // optional
	EnableProfiling                     bool   `split_words:"true"` // optional
	// See https://github.com/knative/serving/issues/12387
	EnableHTTPFullDuplex       bool `split_words:"true"`                      // optional
	EnableHTTP2AutoDetection   bool `envconfig:"ENABLE_HTTP2_AUTO_DETECTION"` // optional
	EnableMultiContainerProbes bool `split_words:"true"`

	// Logging configuration
	ServingLoggingConfig         string `split_words:"true" required:"true"`
	ServingLoggingLevel          string `split_words:"true" required:"true"`
	ServingRequestLogTemplate    string `split_words:"true"` // optional
	ServingEnableRequestLog      bool   `split_words:"true"` // optional
	ServingEnableProbeRequestLog bool   `split_words:"true"` // optional

	// Metrics configuration
	ServingRequestMetricsBackend                string `split_words:"true"` // optional
	ServingRequestMetricsReportingPeriodSeconds int    `split_words:"true"` // optional
	MetricsCollectorAddress                     string `split_words:"true"` // optional

	// Tracing configuration
	TracingConfigDebug          bool                      `split_words:"true"` // optional
	TracingConfigBackend        tracingconfig.BackendType `split_words:"true"` // optional
	TracingConfigSampleRate     float64                   `split_words:"true"` // optional
	TracingConfigZipkinEndpoint string                    `split_words:"true"` // optional

	Env
}

// Env exposes parsed QP environment variables for use by Options (QP Extensions)
type Env struct {
	// ServingNamespace is the namespace in which the service is defined
	ServingNamespace string `split_words:"true" required:"true"`

	// ServingService is the name of the service served by this pod
	ServingService string `split_words:"true"` // optional

	// ServingConfiguration is the name of service configuration served by this pod
	ServingConfiguration string `split_words:"true" required:"true"`

	// ServingRevision is the name of service revision served by this pod
	ServingRevision string `split_words:"true" required:"true"`

	// ServingPod is the pod name
	ServingPod string `split_words:"true" required:"true"`

	// ServingPodIP is the pod ip address
	ServingPodIP string `split_words:"true" required:"true"`

	LeaderPp string `split_words:"true"` // optional
	LeaderKp string `split_words:"true"` // optional
	MemberKp string `split_words:"true"` // optional

	ClientPp     string `split_words:"true"` // optional
	ClientPk     string `split_words:"true"` // optional
	FunctionMode string `split_words:"true"` // optional

	AttachSignature bool `split_words:"true"` // optional
	VerifySignature bool `split_words:"true"` // optional
	DisableLogging  bool `split_words:"true"` // optional

	// Embedded enclave files (loaded from filesystem, not env vars)
	ClientPkPem    []byte            // Contents of /client-pk.pem
	GenesisHash    []byte            // Contents of /genesis.hash
	GenesisHashSig []byte            // Contents of /genesis.hash.sig
	ClientPubKey   ed25519.PublicKey // Parsed Ed25519 public key from ClientPkPem

	// Enclave-generated Ed25519 keypair for signing hashes
	EnclavePublicKey  ed25519.PublicKey  // Generated public key for verification
	EnclavePrivateKey ed25519.PrivateKey // Generated private key for signing

	// Enclave attestation report binding all enclave keys (Ed25519 + BGLS) and podId to the enclave
	EnclaveAttestationReport []byte

	// BGLS03 aggregate signature keys (generated when AttachSignature is enabled)
	SigPp *bgls03.PublicParams // BGLS public params for signature scheme
	SigPk *bgls03.PublicKey    // BGLS public key for verification
	SigSk *bgls03.PrivateKey   // BGLS private key for signing
}

// Defaults provides Options (QP Extensions) with the default bahaviour of QP
// Some attributes of Defaults may be modified by Options
// Modifying Defaults mutates the behavior of QP
type Defaults struct {
	// Logger enables Options to use the QP pre-configured logger
	// It is expected that Options will use the provided Logger when logging
	// Options should not modify the provided Default Logger
	Logger *zap.SugaredLogger

	// Env exposes parsed QP environment variables for use by Options
	// Options should not modify the provided environment parameters
	Env Env

	// Ctx provides Options with the QP context
	// An Option may derive a new context from Ctx. If a new context is derived,
	// the derived context should replace the value of Ctx.
	// The new Ctx will then be used by other Options (called next) and by QP.
	Ctx context.Context

	// Transport provides Options with the QP RoundTripper
	// An Option may wrap the provided Transport to add a Roundtripper.
	// If Transport is wrapped, the new RoundTripper should replace the value of Transport.
	// The new Transport will then be used by other Options (called next) and by QP.
	Transport http.RoundTripper

	// allows queue-proxy to access keys stored in etcd registry
	// holds proxy re-encryption related state
	KeyRegistry *kregistry.KeyRegistry
}

type Option func(*Defaults)

func init() {
	maxprocs.Set()
}

// tries to acquire a lease for this function revision
// once a lease is acquired this queue-proxy acts as the leader
// and will create new public params for proxy re-encryption
// all the other replicas will use the leader's public params
// all the other replicas will depend on the leader for re-encryption key
// other functions encrypting to this revision (function) must use the
// leader's public key for encryption
// src: https://github.com/kubernetes/client-go/blob/master/examples/leader-election/main.go
func TryAcquireLease(d *Defaults) {
	logDev := mutil.LogWithPrefix("dev - TryAcquireLease")

	bgCtx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	ctx, _ := injection.EnableInjectionOrDie(bgCtx, nil)

	// this is pod name
	myId := d.Env.ServingPod
	// lease lock name is the function revision name
	// leaseLockName := d.Env.ServingRevision
	leaseLockName := d.KeyRegistry.GetFunctionId(d.Env.ServingRevision)
	leaseLockNamespace := d.Env.ServingNamespace
	logDev("ServingRevision: %s, ServingNamespace: %s, ServingPod: %s", leaseLockName, leaseLockNamespace, myId)

	// we use the Lease lock type since edits to Leases are less common
	// and fewer objects in the cluster watch "all Leases".
	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Name:      leaseLockName,
			Namespace: leaseLockNamespace,
		},
		Client: kubeclient.Get(ctx).CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: myId,
		},
	}

	// start the leader election code loop
	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock: lock,
		// IMPORTANT: you MUST ensure that any code you have that
		// is protected by the lease must terminate **before**
		// you call cancel. Otherwise, you could have a background
		// loop still running and another process could
		// get elected before your background loop finished, violating
		// the stated goal of the lease.
		ReleaseOnCancel: true,
		LeaseDuration:   15 * time.Second,
		RenewDeadline:   10 * time.Second,
		RetryPeriod:     2 * time.Second,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				// we're notified when we start leading
				d.KeyRegistry.StartedLeading.Store(true)

				logDev := mutil.LogWithPrefix("dev - TryAcquireLease - OnStartedLeading")

				// Old approach: explicitly watch member public keys (replaced by hash chain watcher)
				// The hash chain watcher now receives member public keys as entries
				// at lambada/audit/entry/<idx> and processes them in handleEntryEvent()
				// memberPublicKeyDir := "members/" + myId + "/publicKey"
				// go d.KeyRegistry.ListWatchMemberPublicKeys(memberPublicKeyDir, myId)

				var err error
				var pp *pre.PublicParams
				var keyPair *pre.KeyPair

				// instead of randomly generating key pair and pp
				// use pre-generated key pair and pp from env variables
				leaderPublicParamsSerialized := d.Env.LeaderPp
				leaderKeyPairSerialized := d.Env.LeaderKp

				if leaderPublicParamsSerialized != "" || leaderKeyPairSerialized != "" {
					pp, err = samba.ParsePublicParams([]byte(leaderPublicParamsSerialized))
					if err != nil {
						logDev("Error parsing leader public params from env var: %v", err)
					}
					keyPair, err = samba.ParseKeyPair([]byte(leaderKeyPairSerialized))
					if err != nil {
						logDev("Error parsing leader key pair from env var: %v", err)
					}
				}

				if pp == nil || keyPair == nil {
					logDev("Leader public params or key pair is nil, generating new keys...")
					pp = pre.NewPublicParams()
					keyPair = pre.KeyGen(pp)
				} else {
					logDev("Using pre-generated leader public params and key pair from env variables")
				}

				d.KeyRegistry.SafeWriteLeaderKeys(keyPair, pp)

				// if I'm a leader I'm ready to receive messages as soon as my key pair is ready
				go d.KeyRegistry.MarkPodPreReady()

				lPublicParamsLabel := "leaders/" + d.KeyRegistry.ServiceName +
					"/" + d.KeyRegistry.FunctionId + "/publicParams/" + myId
				lPublicKeyLabel := "leaders/" + d.KeyRegistry.ServiceName +
					"/" + d.KeyRegistry.FunctionId + "/publicKey/" + myId

				// Old plain storage methods (commented out - replaced with hash chain storage)
				// err = d.KeyRegistry.StorePublicKey(lPublicKeyLabel, keyPair.PK)
				// if err != nil {
				// 	logDev("Error storing public key in KeyRegistry: %v", err)
				// }
				// err = d.KeyRegistry.StorePublicParams(lPublicParamsLabel, pp)
				// if err != nil {
				// 	logDev("Error storing public params in KeyRegistry: %v", err)
				// }

				// Store leader public key in hash chain for tamper-evident verification
				// Must serialize curve points properly before JSON marshaling
				pks := new(samba.PublicKeySerialized)
				pks.Serialize(keyPair.PK)
				err = d.KeyRegistry.StoreWithHashChainAndRetry(lPublicKeyLabel, pks, 100)
				if err != nil {
					logDev("Error storing public key with hash chain: %v", err)
				} else {
					logDev("Successfully stored public key with hash chain: %s", lPublicKeyLabel)
				}

				// Store leader public params in hash chain for tamper-evident verification
				// Must serialize curve points properly before JSON marshaling
				pps := new(samba.PublicParamsSerialized)
				pps.Serialize(pp)
				err = d.KeyRegistry.StoreWithHashChainAndRetry(lPublicParamsLabel, pps, 100)
				if err != nil {
					logDev("Error storing public params with hash chain: %v", err)
				} else {
					logDev("Successfully stored public params with hash chain: %s", lPublicParamsLabel)
				}
			},
			OnStoppedLeading: func() {
				// we can do cleanup here, but note that this callback is always called
				// when the LeaderElector exits, even if it did not start leading.
				// Therefore, we should check if we actually started leading before
				// performing any cleanup operations to avoid unexpected behavior.
				logDev("leader lost: %s", myId)

				// Example check to ensure we only perform cleanup if we actually started leading
				if d.KeyRegistry.StartedLeading.Load() {
					// Perform cleanup operations here
					// For example, releasing resources, closing connections, etc.
					logDev("Performing cleanup operations...")
					d.KeyRegistry.StartedLeading.Store(false)
				} else {
					logDev("No cleanup needed as we never started leading.")
				}
				// TODO: think about if we need to exit here
				// TODO: why did I exit here? I don't think exiting here is a good idea
				// TODO: something is definitely wrong here
				os.Exit(0)
				// return
			},
			OnNewLeader: func(leaderIdentity string) {
				// we're notified when new leader elected
				logDev := mutil.LogWithPrefix("dev - TryAcquireLease - OnNewLeader")

				// Old approach: explicitly watch every leader's public keys (replaced by hash chain watcher)
				// The hash chain watcher now receives leader public keys/params as entries
				// at lambada/audit/entry/<idx> and processes them in handleEntryEvent()
				// via processVerifiedEveryLeaderKeys() which stores by service name.
				// go d.KeyRegistry.ListWatchEveryLeaderPublicKeys("leaders/")

				// identity is pod id
				if leaderIdentity == myId {
					// I just got the lock
					// the rest of the code in this callback is mainly for
					// members, if I'm a leader, I can skip it
					logDev("I am the new leader: %s", leaderIdentity)
					return
				}
				logDev("new leader elected: %s", leaderIdentity)
				d.KeyRegistry.SafeWriteMemLeaderId(leaderIdentity)

				// Old approach: explicitly watch re-encryption keys (replaced by hash chain watcher)
				// The hash chain watcher now receives re-encryption keys as entries
				// at lambada/audit/entry/<idx> and processes them in handleEntryEvent()
				// reEncKeyDir := "members/" + leaderIdentity + "/reEncryptionKey/" + myId
				// go d.KeyRegistry.ListWatchReEncryptionKey(reEncKeyDir, leaderIdentity)

				// Old approach variables (commented out - now using hash chain watcher)
				// myFunctionRevision := d.KeyRegistry.FunctionId
				// myService := d.KeyRegistry.ServiceName
				// leader publicKey and publicParams are at:
				// leaders/<service-name>/<function-revision>/publicKey/<leader-pod-id>
				// leaders/<service-name>/<function-revision>/publicParams/<leader-pod-id>
				// leaderPublicPrefix := "leaders/" + myService + "/" + myFunctionRevision + "/public"

				// Old approach: explicitly watch leader keys (replaced by hash chain watcher)
				// The hash chain watcher now receives leader public keys/params as entries
				// at lambada/audit/entry/<idx> and processes them in handleEntryEvent()
				// go d.KeyRegistry.ListWatchLeaderKeys(leaderPublicPrefix, leaderIdentity)
			},
		},
	})
}

func initEtcdWithRetry(d *Defaults) {
	d.KeyRegistry.InitEtcdWithRetry()
}

// generateEnclaveKeypair generates or loads all enclave cryptographic keys.
// This includes:
// - Ed25519 keypair for hash chain signatures
// - BGLS03 signature keys (when AttachSignature is enabled)
//
// It first tries to load sealed keys from disk (to survive pod restarts).
// If no sealed keys exist, it generates new ones and seals them for future restarts.
//
// The attestation report binds ALL keys (Ed25519 + BGLS) and podId to the enclave.
// Report data format: SHA256(podId || ed25519PubKey || bglsPublicKeyMaterial)
func generateEnclaveKeypair(env *Env) {
	logDev := mutil.LogWithPrefix("dev - generateEnclaveKeypair")

	podId := env.ServingPod
	signatureEnabled := env.AttachSignature

	if podId == "" {
		logDev("ServingPod not set, generating keypair without persistence")
		generateNewKeypair(env, podId, signatureEnabled)
		return
	}

	// Try to load existing sealed keys (includes both Ed25519 and BGLS if present)
	unsealedKeys, err := kregistry.UnsealEnclaveKeypairWithSignature(podId)
	if err != nil {
		logDev("Error unsealing keypair (will generate new): %v", err)
	}

	if unsealedKeys != nil && unsealedKeys.PublicKey != nil && unsealedKeys.PrivateKey != nil {
		// Successfully loaded existing Ed25519 keypair
		env.EnclavePublicKey = unsealedKeys.PublicKey
		env.EnclavePrivateKey = unsealedKeys.PrivateKey
		env.EnclaveAttestationReport = unsealedKeys.AttestationReport
		logDev("Loaded sealed enclave Ed25519 keypair for podId=%s (public key: %d bytes)", podId, len(unsealedKeys.PublicKey))

		// Handle BGLS signature keys based on current and sealed state
		needsReseal := false

		if signatureEnabled {
			if unsealedKeys.SignatureEnabled && unsealedKeys.SigPp != nil && unsealedKeys.SigPk != nil && unsealedKeys.SigSk != nil {
				// Sealed data has signature keys - use them
				env.SigPp = unsealedKeys.SigPp
				env.SigPk = unsealedKeys.SigPk
				env.SigSk = unsealedKeys.SigSk
				logDev("Loaded sealed BGLS signature keys for podId=%s", podId)
			} else {
				// Signature is enabled now but wasn't before (or keys are corrupted)
				// Generate new BGLS keys and create new attestation report
				logDev("Signature enabled but sealed data missing BGLS keys - generating new ones")
				if generateBglsSignatureKeys(env) {
					// Need to regenerate attestation report with both key types
					regenerateAttestationReport(env, podId)
					needsReseal = true
				}
			}
		} else {
			// Signature is disabled now - don't load BGLS keys even if present
			if unsealedKeys.SignatureEnabled {
				logDev("Signature was previously enabled but is now disabled - BGLS keys not loaded")
				// Note: We could reseal without signature keys, but we keep the existing sealed data
				// to allow re-enabling signature later without regenerating keys
			}
		}

		// Reseal if we generated new BGLS keys
		if needsReseal && env.EnclavePublicKey != nil && env.EnclavePrivateKey != nil {
			if err := kregistry.SealEnclaveKeypairWithSignature(
				podId,
				env.EnclavePublicKey,
				env.EnclavePrivateKey,
				env.EnclaveAttestationReport,
				signatureEnabled,
				env.SigPp,
				env.SigPk,
				env.SigSk,
			); err != nil {
				logDev("Warning: failed to reseal keypair with signature keys: %v", err)
			} else {
				logDev("Resealed keypair with new BGLS signature keys")
			}
		}
		return
	}

	// No existing keypair, generate new ones
	generateNewKeypair(env, podId, signatureEnabled)

	// Seal the new keypair for future restarts
	if env.EnclavePublicKey != nil && env.EnclavePrivateKey != nil {
		if err := kregistry.SealEnclaveKeypairWithSignature(
			podId,
			env.EnclavePublicKey,
			env.EnclavePrivateKey,
			env.EnclaveAttestationReport,
			signatureEnabled,
			env.SigPp,
			env.SigPk,
			env.SigSk,
		); err != nil {
			logDev("Warning: failed to seal keypair (pod restart will generate new keys): %v", err)
		}
	}
}

// generateBglsSignatureKeys generates BGLS03 signature keys if AttachSignature is enabled.
// Returns true if keys were successfully generated.
func generateBglsSignatureKeys(env *Env) bool {
	logDev := mutil.LogWithPrefix("dev - generateBglsSignatureKeys")

	if !env.AttachSignature {
		logDev("AttachSignature is disabled, skipping BGLS key generation")
		return false
	}

	// Parse BGLS public params from environment variable
	pp, err := mutil.ParseSignaturePublicParams()
	if err != nil {
		logDev("Failed to parse BGLS public params from env: %v", err)
		logDev("BGLS signature keys will not be generated")
		return false
	}

	// Generate BGLS keypair
	pk, sk := bgls03.KeyGen(pp)
	if pk == nil || sk == nil {
		logDev("BGLS KeyGen returned nil keys")
		return false
	}

	env.SigPp = pp
	env.SigPk = pk
	env.SigSk = sk
	logDev("Successfully generated BGLS03 signature keys")
	return true
}

// regenerateAttestationReport creates a new attestation report that binds
// both Ed25519 and BGLS keys to the enclave.
func regenerateAttestationReport(env *Env, podId string) {
	logDev := mutil.LogWithPrefix("dev - regenerateAttestationReport")

	if podId == "" {
		logDev("ServingPod not set, skipping attestation report regeneration")
		return
	}

	reportData := computeAttestationReportData(env, podId)

	report, err := enclave.GetRemoteReport(reportData)
	if err != nil {
		logDev("Failed to get attestation report from ego: %v", err)
		return
	}

	env.EnclaveAttestationReport = report
	logDev("Successfully regenerated attestation report (%d bytes) binding all keys to enclave", len(report))
}

// computeAttestationReportData computes the hash that binds all enclave keys.
// Format: SHA256(podId || ed25519PubKey || bglsPkmHash)
// where bglsPkmHash = SHA256(JSON(pp, pk, podId)) if signature is enabled
func computeAttestationReportData(env *Env, podId string) []byte {
	logDev := mutil.LogWithPrefix("dev - computeAttestationReportData")

	h := sha256.New()
	h.Write([]byte(podId))
	h.Write(env.EnclavePublicKey)

	// Include BGLS public key material if signature is enabled
	if env.AttachSignature && env.SigPp != nil && env.SigPk != nil {
		pkmHash, err := bgls.HashPkm(env.SigPp, env.SigPk, podId)
		if err != nil {
			logDev("Failed to compute BGLS PKM hash: %v", err)
		} else {
			// Hash the PKM hash again for uniform size
			bglsHash := sha256.Sum256(pkmHash)
			h.Write(bglsHash[:])
			logDev("Included BGLS public key material in attestation report data")
		}
	}

	return h.Sum(nil)
}

// generateNewKeypair creates all new enclave keys and a unified attestation report.
func generateNewKeypair(env *Env, podId string, signatureEnabled bool) {
	logDev := mutil.LogWithPrefix("dev - generateNewKeypair")

	// Generate Ed25519 keypair
	pubKey, privKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		logDev("Error generating Ed25519 keypair: %v", err)
		return
	}

	env.EnclavePublicKey = pubKey
	env.EnclavePrivateKey = privKey
	logDev("Successfully generated enclave Ed25519 keypair (public key: %d bytes)", len(pubKey))

	// Generate BGLS signature keys if enabled
	if signatureEnabled {
		generateBglsSignatureKeys(env)
	}

	if podId == "" {
		logDev("ServingPod not set, skipping attestation report generation")
		return
	}

	// Compute attestation report data that binds ALL keys
	reportData := computeAttestationReportData(env, podId)

	logDev("Generating unified attestation report binding podId=%s, Ed25519 key, and BGLS keys (if enabled)", podId)

	// Generate attestation report with the combined hash as report data
	report, err := enclave.GetRemoteReport(reportData)
	if err != nil {
		logDev("Failed to get attestation report from ego: %v", err)
		// Continue without attestation - the keypairs are still usable
		return
	}

	env.EnclaveAttestationReport = report
	logDev("Successfully generated unified attestation report (%d bytes)", len(report))
}

// loadEnclaveEmbeddedFiles reads the files embedded in the enclave (defined in enclave.json)
// and stores their contents in the Env struct for later access.
func loadEnclaveEmbeddedFiles(env *Env) {
	logDev := mutil.LogWithPrefix("dev - loadEnclaveEmbeddedFiles")

	embeddedFiles := []struct {
		path   string
		target *[]byte
		name   string
	}{
		{"/client-pk.pem", &env.ClientPkPem, "ClientPkPem"},
		{"/genesis.hash", &env.GenesisHash, "GenesisHash"},
		{"/genesis.hash.sig", &env.GenesisHashSig, "GenesisHashSig"},
	}

	for _, f := range embeddedFiles {
		if _, err := os.Stat(f.path); os.IsNotExist(err) {
			logDev("Embedded file %s does not exist in enclave memory", f.path)
			continue
		}

		data, err := os.ReadFile(f.path)
		if err != nil {
			logDev("Error reading embedded file %s: %v", f.path, err)
			continue
		}

		*f.target = data
		logDev("Successfully loaded embedded file %s (%d bytes)", f.path, len(data))
		logDev("  %s contents: %s", f.name, string(data))
	}

	// Parse the Ed25519 public key from the PEM file
	if len(env.ClientPkPem) > 0 {
		pubKey, err := parseEd25519PublicKey(env.ClientPkPem)
		if err != nil {
			logDev("Error parsing Ed25519 public key: %v", err)
		} else {
			env.ClientPubKey = pubKey
			logDev("Successfully parsed Ed25519 public key (%d bytes)", len(pubKey))
		}
	}

	// Verify the genesis hash signature using the client's public key
	if len(env.GenesisHash) > 0 && len(env.GenesisHashSig) > 0 && env.ClientPubKey != nil {
		// Decode hex-encoded hash and signature back to binary
		hashBytes, err := hex.DecodeString(string(bytes.TrimSpace(env.GenesisHash)))
		if err != nil {
			logDev("Error decoding GenesisHash from hex: %v", err)
		} else {
			sigBytes, err := hex.DecodeString(string(bytes.TrimSpace(env.GenesisHashSig)))
			if err != nil {
				logDev("Error decoding GenesisHashSig from hex: %v", err)
			} else {
				if ed25519.Verify(env.ClientPubKey, hashBytes, sigBytes) {
					logDev("Genesis hash signature verification PASSED")
				} else {
					logDev("ERROR: Genesis hash signature verification FAILED - signature does not match")
				}
			}
		}
	} else {
		if len(env.GenesisHash) == 0 {
			logDev("Skipping signature verification: GenesisHash not loaded")
		}
		if len(env.GenesisHashSig) == 0 {
			logDev("Skipping signature verification: GenesisHashSig not loaded")
		}
		if env.ClientPubKey == nil {
			logDev("Skipping signature verification: ClientPubKey not available")
		}
	}

	logDev("Finished loading enclave embedded files")
}

// parseEd25519PublicKey parses an Ed25519 public key from PEM-encoded data
func parseEd25519PublicKey(pemData []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
	}

	// Try parsing as PKIX public key (standard format)
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		// If PKIX parsing fails, try parsing the raw bytes directly
		// (in case it's just the raw 32-byte Ed25519 public key)
		if len(block.Bytes) == ed25519.PublicKeySize {
			return ed25519.PublicKey(block.Bytes), nil
		}
		return nil, fmt.Errorf("failed to parse public key: %w", err)
	}

	edPub, ok := pub.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("public key is not an Ed25519 key, got %T", pub)
	}

	return edPub, nil
}

// initGenesisHash decodes and sets the genesis hash for the chain watcher.
// This must be called BEFORE any writes to the hash chain to ensure the first
// entry uses the correct genesis hash as its prev_digest.
// Returns the decoded genesis hash bytes for use by publishEnclavePublicKey.
func initGenesisHash(d *Defaults, logger *zap.SugaredLogger) []byte {
	logDev := mutil.LogWithPrefix("dev - initGenesisHash")

	// Decode genesis hash
	var genesisHashBytes []byte
	if len(d.Env.GenesisHash) > 0 {
		var err error
		genesisHashBytes, err = hex.DecodeString(string(bytes.TrimSpace(d.Env.GenesisHash)))
		if err != nil {
			logger.Warnw("Failed to decode genesis hash", zap.Error(err))
		} else {
			logDev("Decoded genesis hash: %x", genesisHashBytes)
		}
	}

	// Set the genesis hash on the chain watcher (before any writes)
	kregistry.SetGenesisHash(genesisHashBytes)
	logDev("Initialized genesis hash for chain watcher")

	return genesisHashBytes
}

func startHashChainWatcher(d *Defaults, logger *zap.SugaredLogger) {
	logDev := mutil.LogWithPrefix("dev - startHashChainWatcher")

	// Decode genesis hash for the watcher
	var genesisHashBytes []byte
	if len(d.Env.GenesisHash) > 0 {
		var err error
		genesisHashBytes, err = hex.DecodeString(string(bytes.TrimSpace(d.Env.GenesisHash)))
		if err != nil {
			logger.Warnw("Failed to decode genesis hash for watcher", zap.Error(err))
		}
	}

	// Start the hash chain watcher
	if err := d.KeyRegistry.StartHashChainWatcher(genesisHashBytes); err != nil {
		logger.Warnw("Failed to start hash chain watcher", zap.Error(err))
	} else {
		logDev("Started hash chain watcher")
	}
}

// publishEnclavePublicKey publishes the enclave's Ed25519 public key and optionally
// BGLS signature keys to the hash chain. This is the single entry point for publishing
// all enclave cryptographic material, ensuring they are bound together in the attestation.
//
// The attestation report binds:
//   - Ed25519 public key (for hash chain signatures)
//   - BGLS public params and public key (for aggregate signatures, if AttachSignature is enabled)
//   - Pod ID (writer identity)
func publishEnclavePublicKey(d *Defaults, logger *zap.SugaredLogger, genesisHash []byte) {
	logDev := mutil.LogWithPrefix("dev - publishEnclavePublicKey")

	// Store enclave public key with hash chain for tamper-evident audit log
	if d.KeyRegistry.EnclavePublicKey == nil || d.KeyRegistry.EnclavePrivateKey == nil {
		logDev("Enclave keypair not available, skipping hash chain storage")
		return
	}

	// Determine if BGLS signature keys should be published
	signatureEnabled := d.Env.AttachSignature
	var sigPp *bgls03.PublicParams
	var sigPk *bgls03.PublicKey

	if signatureEnabled {
		sigPp = d.Env.SigPp
		sigPk = d.Env.SigPk

		if sigPp == nil || sigPk == nil {
			logDev("AttachSignature is enabled but BGLS keys are nil - publishing without signature keys")
			signatureEnabled = false
		} else {
			logDev("Including BGLS signature keys in published enclave public key")
		}
	}

	// Use the verified storage function which:
	// 1. Verifies the entire chain before writing (no watcher needed)
	// 2. Updates the watcher's verified state after successful write
	// 3. Handles retries with exponential backoff
	// 4. Publishes both Ed25519 and BGLS keys in a single atomic operation
	err := d.KeyRegistry.StoreEnclavePublicKeyWithRetryVerified(
		d.KeyRegistry.PodId,
		d.KeyRegistry.EnclavePublicKey,
		d.KeyRegistry.EnclavePrivateKey,
		d.Env.EnclaveAttestationReport,
		genesisHash,
		100, // High retry count for startup conflicts
		signatureEnabled,
		sigPp,
		sigPk,
	)
	if err != nil {
		// This is fatal - without the enclave public key, other pods cannot verify
		// any entries from this pod, which will break hash chain verification.
		logger.Fatalw("Failed to store enclave public key with hash chain after all retries",
			zap.Error(err),
			zap.String("podID", d.KeyRegistry.PodId))
	} else {
		logger.Infow("Successfully stored enclave public key with hash chain",
			zap.String("podID", d.KeyRegistry.PodId),
			zap.Int("attestationBytes", len(d.Env.EnclaveAttestationReport)),
			zap.Bool("signatureEnabled", signatureEnabled))

		// Mark that our enclave public key is now published.
		// This allows deferred member key processing to proceed.
		kregistry.MarkEnclavePublicKeyPublished()
		logDev("Marked enclave public key as published")

		// Process any member keys that were received while waiting for our public key.
		// These were queued because we couldn't write re-encryption keys without
		// other pods being able to verify our signatures.
		// Run in goroutine to avoid blocking the main flow.
		go d.KeyRegistry.ProcessPendingMemberKeys()

		// Note: BGLS signature verification keys are now discovered via getWriterPublicKey()
		// when verifying hash chain entries. No separate watcher needed.
	}
}

func printFilesUnderProc() {
	logDev := mutil.LogWithPrefix("dev - printFilesUnderProc")

	logDev("=== EGO Enclave Environment Debug ===")

	// Check /proc mounts
	if entries, err := os.ReadDir("/proc"); err != nil {
		logDev("ERROR: Cannot read /proc: %v", err)
	} else {
		logDev("/proc contains %d entries", len(entries))
	}

	// Check specific paths
	checkPaths := []string{
		"/proc/net/sockstat",
		"/proc/net/tcp",
		"/proc/net/tcp6",
		"/proc/sys/net/core/somaxconn",
		"/proc/sys/net/ipv4/ip_local_port_range",
	}

	for _, path := range checkPaths {
		if stat, err := os.Stat(path); err != nil {
			logDev("MISSING: %s - %v", path, err)
		} else {
			logDev("EXISTS: %s (mode: %v)", path, stat.Mode())
			// Try to read it
			if data, err := os.ReadFile(path); err != nil {
				logDev("  Cannot read: %v", err)
			} else {
				logDev("  Content: %s", string(data)[:min(100, len(data))])
			}
		}
	}

	logDev("=== End Debug ===")
}

func startResourceMonitoring(logger *zap.SugaredLogger) {
	ticker := time.NewTicker(10 * time.Second)
	go func() {
		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			logger.Infof("Resource Stats: NumGoroutine=%d, Alloc=%dMB, Sys=%dMB, NumGC=%d",
				runtime.NumGoroutine(),
				m.Alloc/1024/1024,
				m.Sys/1024/1024,
				m.NumGC)
		}
	}()
}

func Main(opts ...Option) error {
	// printFilesUnderProc()
	// startResourceMonitoring()

	d := Defaults{
		Ctx: signals.NewContext(),
	}

	// Parse the environment.
	var env config
	if err := envconfig.Process("", &env); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}

	// NOTE: d.Env is very very useful
	d.Env = env.Env

	// Generate Ed25519 keypair for signing hashes within the enclave
	generateEnclaveKeypair(&d.Env)

	// Load enclave embedded files
	// These files are defined in dev/queue-proxy/enclave.json
	loadEnclaveEmbeddedFiles(&d.Env)

	// Setup the Logger.
	logger, _ := pkglogging.NewLogger(env.ServingLoggingConfig, env.ServingLoggingLevel)
	defer flush(logger)

	// startResourceMonitoring(logger)

	logDev := mutil.LogWithPrefix("dev - Main")
	logDev("d.Env = %+v", d.Env)

	d.KeyRegistry = new(kregistry.KeyRegistry)
	d.KeyRegistry.IsEtcdReady = make(chan struct{})
	d.KeyRegistry.LoadMyEnvVars(d.Env.ServingService)

	// Note: BGLS signature keys are generated in generateEnclaveKeypair() and published
	// together with the Ed25519 public key in publishEnclavePublicKey(). This ensures
	// all enclave keys are bound together in a single attestation report.
	// go d.KeyRegistry.SetupSignature(env.AttachSignature, env.ServingPod)

	// connect to etcd
	go initEtcdWithRetry(&d)

	logger = logger.Named("queueproxy").With(
		zap.String(logkey.Key, types.NamespacedName{
			Namespace: env.ServingNamespace,
			Name:      env.ServingRevision,
		}.String()),
		zap.String(logkey.Pod, env.ServingPod))

	d.Logger = logger
	d.Transport = buildTransport(env)

	d.Transport = &DebugTransport{
		Transport:   d.Transport,
		KeyRegistry: d.KeyRegistry,
	}

	if env.TracingConfigBackend != tracingconfig.None {
		oct := tracing.NewOpenCensusTracer(tracing.WithExporterFull(env.ServingPod, env.ServingPodIP, logger))
		oct.ApplyConfig(&tracingconfig.Config{
			Backend:        env.TracingConfigBackend,
			Debug:          env.TracingConfigDebug,
			ZipkinEndpoint: env.TracingConfigZipkinEndpoint,
			SampleRate:     env.TracingConfigSampleRate,
		})
		defer oct.Shutdown(context.Background())
	}

	// allow extensions to read d and return modified context and transport
	opts = append(opts, initKeyRegistry())
	for _, opts := range opts {
		opts(&d)
	}

	// Report stats on Go memory usage every 30 seconds.
	metrics.MemStatsOrDie(d.Ctx)

	protoStatReporter := queue.NewProtobufStatsReporter(env.ServingPod, reportingPeriod)

	reportTicker := time.NewTicker(reportingPeriod)
	defer reportTicker.Stop()

	stats := netstats.NewRequestStats(time.Now())
	go func() {
		for now := range reportTicker.C {
			stat := stats.Report(now)
			protoStatReporter.Report(stat)
		}
	}()

	// Setup probe to run for checking user-application healthiness.
	probe := func() bool { return true }
	if env.ServingReadinessProbe != "" {
		probe = buildProbe(logger, env.ServingReadinessProbe, env.EnableHTTP2AutoDetection, env.EnableMultiContainerProbes).ProbeContainer
	}

	// Enable TLS when certificate is mounted.
	tlsEnabled := exists(logger, certPath) && exists(logger, keyPath)
	logger.Infof("[dev] TLS enabled: %v (cert: %s, key: %s)", tlsEnabled, certPath, keyPath)

	// what does mainHandler do?
	mainHandler, drainer := mainHandler(d.Ctx, env, d.Transport, probe, stats, logger, d.KeyRegistry)
	adminHandler := adminHandler(d.Ctx, logger, drainer)

	// Enable TLS server when activator server certs are mounted.
	// At this moment activator with TLS does not disable HTTP.
	// See also https://github.com/knative/serving/issues/12808.
	httpServers := map[string]*http.Server{
		"main":    mainServer(":"+env.QueueServingPort, mainHandler),
		"admin":   adminServer(":"+strconv.Itoa(networking.QueueAdminPort), adminHandler),
		"metrics": metricsServer(protoStatReporter),
	}

	if env.EnableProfiling {
		httpServers["profile"] = profiling.NewServer(profiling.NewHandler(logger, true))
	}

	tlsServers := make(map[string]*http.Server)
	var certWatcher *certificate.CertWatcher
	var err error

	if tlsEnabled {
		tlsServers["main"] = mainServer(":"+env.QueueServingTLSPort, mainHandler)
		tlsServers["admin"] = adminServer(":"+strconv.Itoa(networking.QueueAdminPort), adminHandler)

		certWatcher, err = certificate.NewCertWatcher(certPath, keyPath, 1*time.Minute, logger)
		if err != nil {
			logger.Fatal("failed to create certWatcher", zap.Error(err))
		}
		defer certWatcher.Stop()

		// Drop admin http server since the admin TLS server is listening on the same port
		delete(httpServers, "admin")
	}

	// before queue-proxy starts listening for requests
	d.KeyRegistry.IsPodPreReady = make(chan struct{})
	go TryAcquireLease(&d)

	// once etcd is ready fetch the static function chains
	<-d.KeyRegistry.IsEtcdReady
	d.KeyRegistry.FetchStaticFunctionChains()

	// CRITICAL: Initialize genesis hash BEFORE any writes to the hash chain.
	// This ensures the first entry uses the correct genesis hash as prev_digest.
	genesisHashBytes := initGenesisHash(&d, logger)

	// Publish enclave public key FIRST, using verified storage.
	// The StoreEnclavePublicKeyWithRetryVerified function:
	// 1. Verifies the entire chain before writing (no watcher needed)
	// 2. Updates the watcher's verified state after successful write
	// 3. Handles retries with exponential backoff
	//
	// This solves the chicken-and-egg problem: we can publish our key without
	// the watcher running, and the watcher will start from the already-verified state.
	publishEnclavePublicKey(&d, logger, genesisHashBytes)

	// Start hash chain watcher AFTER publishing enclave public key.
	// The watcher continues from the state already verified during publish.
	// It will process any member keys that arrive after our public key is published.
	startHashChainWatcher(&d, logger)

	// is pod read for proxy re-encryption?
	// wait until this pod becomes the leader or joins as a member
	<-d.KeyRegistry.IsPodPreReady

	// THINK: with all the leader election, etcd connection with retry,
	// re-encryption key generation, and running on top of enclaves -
	// are there ways we can speed up the queue-proxy startup?
	logger.Info("Starting queue-proxy")

	errCh := make(chan error)
	for name, server := range httpServers {
		logDev("Installed http server with name: %v", name)
		go func(name string, s *http.Server) {
			// Don't forward ErrServerClosed as that indicates we're already shutting down.
			logger.Info("Starting http server ", name, s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s server failed to serve: %w", name, err)
			}
		}(name, server)
	}
	// no tls servers seen on logs
	for name, server := range tlsServers {
		go func(name string, s *http.Server) {
			logger.Info("Starting tls server ", name, s.Addr)
			s.TLSConfig = &tls.Config{
				GetCertificate: certWatcher.GetCertificate,
				MinVersion:     tls.VersionTLS13,
			}
			// Don't forward ErrServerClosed as that indicates we're already shutting down.
			if err := s.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("%s server failed to serve: %w", name, err)
			}
		}(name, server)
	}

	logger.Infof("[dev] scanned queue-proxy with config: %+v", env)

	// Blocks until we actually receive a TERM signal or one of the servers
	// exits unexpectedly. We fold both signals together because we only want
	// to act on the first of those to reach here.
	select {
	case err := <-errCh:
		logger.Errorw("Failed to bring up queue-proxy, shutting down.", zap.Error(err))
		return err
	case <-d.Ctx.Done():
		logger.Info("Received TERM signal, attempting to gracefully shutdown servers.")
		logger.Infof("Sleeping %v to allow K8s propagation of non-ready state", drainSleepDuration)
		drainer.Drain()

		for name, srv := range httpServers {
			logger.Info("Shutting down server: ", name)
			if err := srv.Shutdown(context.Background()); err != nil {
				logger.Errorw("Failed to shutdown server", zap.String("server", name), zap.Error(err))
			}
		}
		for name, srv := range tlsServers {
			logger.Info("Shutting down server: ", name)
			if err := srv.Shutdown(context.Background()); err != nil {
				logger.Errorw("Failed to shutdown server", zap.String("server", name), zap.Error(err))
			}
		}

		logger.Info("Shutdown complete, exiting...")
	}
	return nil
}

// initialize proxy re-encryption values
// initKeyRegistry initializes the KeyRegistry with enclave keys and identifiers.
// This includes both Ed25519 keys for hash chain signatures and BGLS03 keys
// for aggregate signatures (when AttachSignature is enabled).
func initKeyRegistry() Option {
	return func(d *Defaults) {
		d.KeyRegistry.InstanceId = d.Env.ServingPodIP
		d.KeyRegistry.FunctionId = d.KeyRegistry.GetFunctionId(d.Env.ServingRevision)
		d.KeyRegistry.ServiceName = d.KeyRegistry.GetServiceName(d.Env.ServingService)
		d.KeyRegistry.PodId = d.Env.ServingPod

		// Ed25519 keys for hash chain signatures
		d.KeyRegistry.EnclavePublicKey = d.Env.EnclavePublicKey
		d.KeyRegistry.EnclavePrivateKey = d.Env.EnclavePrivateKey

		// BGLS03 signature keys (set by generateEnclaveKeypair when AttachSignature is enabled)
		if d.Env.AttachSignature && d.Env.SigPp != nil && d.Env.SigPk != nil && d.Env.SigSk != nil {
			d.KeyRegistry.SigPp = d.Env.SigPp
			d.KeyRegistry.SigPk = d.Env.SigPk
			d.KeyRegistry.SigSk = d.Env.SigSk
			d.KeyRegistry.AttestationReport = d.Env.EnclaveAttestationReport
		}
	}
}

func exists(logger *zap.SugaredLogger, filename string) bool {
	_, err := os.Stat(filename)
	if err != nil && !os.IsNotExist(err) {
		logger.Fatalw(fmt.Sprintf("Failed to verify the file path %q", filename), zap.Error(err))
	}
	return err == nil
}

func buildProbe(logger *zap.SugaredLogger, encodedProbe string, autodetectHTTP2 bool, multiContainerProbes bool) *readiness.Probe {
	coreProbes, err := readiness.DecodeProbes(encodedProbe, multiContainerProbes)
	if err != nil {
		logger.Fatalw("Queue container failed to parse readiness probe", zap.Error(err))
	}
	if autodetectHTTP2 {
		return readiness.NewProbeWithHTTP2AutoDetection(coreProbes)
	}
	return readiness.NewProbe(coreProbes)
}

type DebugTransport struct {
	Transport   http.RoundTripper
	KeyRegistry *kregistry.KeyRegistry
}

func (d *DebugTransport) decryptRSAMessage(encryptedBytes []byte) ([]byte, error) {
	logDev := mutil.LogWithPrefix("dev - decryptRSAMessage")

	plaintext, err := mutil.RSAHybridDecrypt(d.KeyRegistry.RSASecretKey, encryptedBytes)
	if err != nil {
		logDev("failed to decrypt message using RSA private key: %v", err.Error())
		return nil, err
	}
	return plaintext, nil
}

func (d *DebugTransport) decryptSambaMessage(encryptedBytes []byte) ([]byte, []byte, error) {
	logDev := mutil.LogWithPrefix("dev - decryptSambaMessage")

	var sambaMessage *samba.SambaMessage
	if err := json.Unmarshal(encryptedBytes, &sambaMessage); err != nil {
		logDev("Invalid message format: %v", err)
		return nil, nil, err
	}

	var err error
	var myPublicParams *pre.PublicParams
	var myKeyPair *pre.KeyPair
	var reEncKey *pre.ReEncryptionKey

	isLeader := d.KeyRegistry.StartedLeading.Load()
	if isLeader {
		// no need for re-encryption, just decrypt
		reEncKey = nil
		myPublicParams, myKeyPair = d.KeyRegistry.SafeReadLeaderKeys()
	} else {
		logDev("I'm a member, so re-encrypting message to my keys before decryption")
		// assuming I am a member
		myLeaderId := d.KeyRegistry.SafeReadMemLeaderId()
		// get the re-encryption key
		reEncKey = d.KeyRegistry.SafeReadMemLeaderReEncryptionKey(myLeaderId)

		myKeyPair = d.KeyRegistry.SafeReadMemKeyPair(myLeaderId)
		myPublicParams = d.KeyRegistry.SafeReadMemLeaderPublicParams(myLeaderId)

		// re-encrypt the ciphertext
		sambaMessage, err = mutil.ReEncrypt(myPublicParams, reEncKey, sambaMessage)
		if err != nil {
			logDev("Error re-encrypting message: %v", err)
			return nil, nil, err
		}
	}

	// decrypt the ciphertext, get the plaintext
	plaintext, err := mutil.Decrypt(myPublicParams, myKeyPair.SK, sambaMessage)
	if err != nil {
		logDev("Error decrypting message: %v", err)
		return nil, nil, err
	}

	signature, err := mutil.DecryptSignature(myPublicParams, myKeyPair.SK, sambaMessage)
	if err != nil {
		logDev("Error decrypting message: %v", err)
		return nil, nil, err
	}

	return plaintext, signature, nil
}

// decrypts the response for user-container
func (d *DebugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	logDev := mutil.LogWithPrefix("dev - RoundTrip")
	// start := time.Now()
	// defer func() {
	// 	logDev("RoundTrip took %v", time.Since(start))
	// }()

	logDev("Request: %s %s\n", req.Method, req.URL.String())
	for name, values := range req.Header {
		for _, value := range values {
			logDev("  %s: %s\n", name, value)
		}
	}

	// who is sending the GET request with nil body?
	if req.Body == nil || req.ContentLength == 0 {
		logDev("Request body is empty, skipping decryption logic.")
		return d.Transport.RoundTrip(req)
	}
	defer req.Body.Close()

	nonce := req.Header.Get("Ce-Nonce")
	if nonce == "" {
		logDev("Request missing Ce-Nonce header, cannot add signature.")
	}

	// records the function instance ids that have signed this message
	funChain := req.Header.Get("Ce-Functionchain")
	logDev("Existing function chain: %s", funChain)

	encBody, err := io.ReadAll(req.Body)
	if err != nil {
		logDev("Error reading request body: %v", err)
		return nil, err
	}
	logDev("Request Body (encrypted): %s", string(encBody))

	var plaintext []byte
	var signatureBytes []byte

	functionMode := mutil.GetFunctionMode()

	switch functionMode {
	case mutil.FunctionModeEmpty:
		logDev("Function mode is undefined, skipping decryption logic.")
		plaintext = encBody

	case mutil.FunctionModeFake:
		logDev("Function mode is FAKE, using FakeDecrypt method.")
		plaintext, err = mutil.FakeDecrypt(encBody)
		if err != nil {
			logDev("Error decrypting request body: %v", err)
			return nil, err
		}

	case mutil.FunctionModeSingle:
		logDev("Function mode is SINGLE, use my secret key to decrypt message.")

		// check if RSA private key is set
		// if yes use it to decrypt instead of samba
		if d.KeyRegistry.RSASecretKey != nil {
			logDev("Decrypting message using RSA private key.")
			plaintext, err = d.decryptRSAMessage(encBody)
			if err != nil {
				logDev("failed to decrypt message using RSA private key: %v", err.Error())
				return nil, err
			}
		} else {
			// decrypt the ciphertext using proxy re-encryption
			logDev("Decrypting message using samba re-encryption.")
			plaintext, signatureBytes, err = d.decryptSambaMessage(encBody)
			if err != nil {
				logDev("Error decrypting message: %v", err)
				return nil, err
			}
		}

	case mutil.FunctionModeChain:
		if d.KeyRegistry.RSASecretKey != nil {
			logDev("Function mode is CHAIN, but RSA private key is set, using RSA decryption instead of samba.")
			plaintext, err = d.decryptRSAMessage(encBody)
			if err != nil {
				logDev("failed to decrypt message using RSA private key: %v", err.Error())
				return nil, err
			}
		} else {
			logDev("Function mode is CHAIN, getting the function chain from env var.")
			chainedServices := d.KeyRegistry.GetFunctionChainFromEnv()
			currentService := d.KeyRegistry.ServiceName
			currentServiceIndex := slices.Index(chainedServices, currentService)
			prevServiceIndex := currentServiceIndex - 1

			if prevServiceIndex < 0 {
				logDev("Message to `first` service is sent encrypted by client")
			}
			logDev("decrypting message for service %s in chain %v", currentService, chainedServices)
			// decrypt the ciphertext, get the plaintext
			plaintext, signatureBytes, err = d.decryptSambaMessage(encBody)
			if err != nil {
				logDev("Error decrypting message: %v", err)
				return nil, err
			}
		}

	default:
		logDev("Function mode is %s, not implemented", functionMode)
		plaintext = encBody
	}

	logDev("Request Body (decrypted) (to user-container): %s", string(plaintext))

	req.Body = io.NopCloser(bytes.NewReader(plaintext))
	req.ContentLength = int64(len(plaintext))
	req.Header.Set("X-Queue-Decrypted", "true")
	req.Header.Set("Content-Length", strconv.Itoa(len(plaintext)))

	sigHex := hex.EncodeToString(signatureBytes)
	req.Header.Set("Ce-Aggsignature", sigHex)

	if os.Getenv("VERIFY_SIGNATURE") == "true" {
		logDev("Verifying signature as VERIFY_SIGNATURE is true.")
		// this is where we verify the signature
		err = d.KeyRegistry.VerifySignature(signatureBytes, funChain, nonce)
		if err != nil {
			logDev("error verifying signature: %v", err)
			return nil, err
		}
	} else {
		logDev("Skipping signature verification as VERIFY_SIGNATURE is %v.", os.Getenv("VERIFY_SIGNATURE"))
	}

	// Flow tracking: record that this service processed this message (nonce/flowID)
	// This enables replay detection - if the same flow is processed twice by the same service, reject it
	if os.Getenv("FLOW_TRACKING_ENABLED") == "true" && nonce != "" {
		logDev("Flow tracking enabled, processing flow: %s", nonce)

		// 1. Check for replay (this service already processed this flow)
		if _, found := kregistry.IsFlowVerified(nonce, d.KeyRegistry.ServiceName); found {
			logDev("Replay detected: flow %s already processed by service %s", nonce, d.KeyRegistry.ServiceName)
			return nil, fmt.Errorf("replay detected: flow %s already processed by this service", nonce)
		}

		// 2. Start async flow recording - runs in background while request is processed
		// The result will be collected in EncryptResponseBody before sending response
		newCtx, _ := d.KeyRegistry.StartFlowRecordingAsync(req.Context(), nonce)
		req = req.WithContext(newCtx)

		// 3. Mark that flow tracking is enabled so EncryptResponseBody knows to wait for result
		req.Header.Set(kregistry.FlowTrackingEnabledHeader, "true")
	} else {
		logDev("Flow tracking disabled or nonce missing, skipping flow processing.")
	}

	if funChain == "" {
		funChain = d.KeyRegistry.PodId
	} else {
		funChain = funChain + "|" + d.KeyRegistry.PodId
	}
	req.Header.Set("Ce-Functionchain", funChain)

	return d.Transport.RoundTrip(req)
}

func buildTransport(env config) http.RoundTripper {
	maxIdleConns := 1000 // TODO: somewhat arbitrary value for CC=0, needs experimental validation.
	if env.ContainerConcurrency > 0 {
		maxIdleConns = env.ContainerConcurrency
	}
	// set max-idle and max-idle-per-host to same value since we're always proxying to the same host.
	transport := pkgnet.NewProxyAutoTransport(maxIdleConns /* max-idle */, maxIdleConns /* max-idle-per-host */)

	// here
	// tlsConfig := enclave.CreateAttestationClientTLSConfig(verifyReport)
	// dialTLSContextFunc := func(ctx context.Context, network, addr string) (net.Conn, error) {
	// 	return pkgnet.DialTLSWithBackOff(ctx, network, addr, tlsConfig)
	// }
	// transport := pkgnet.NewProxyAutoTLSTransport(maxIdleConns, maxIdleConns, dialTLSContextFunc)

	if env.TracingConfigBackend == tracingconfig.None {
		return transport
	}

	return &ochttp.Transport{
		Base:        transport,
		Propagation: tracecontextb3.TraceContextB3Egress,
	}
}

func buildBreaker(logger *zap.SugaredLogger, env config) *queue.Breaker {
	if env.ContainerConcurrency < 1 {
		return nil
	}

	// We set the queue depth to be equal to the container concurrency * 10 to
	// allow the autoscaler time to react.
	queueDepth := 10 * env.ContainerConcurrency
	params := queue.BreakerParams{
		QueueDepth:      queueDepth,
		MaxConcurrency:  env.ContainerConcurrency,
		InitialCapacity: env.ContainerConcurrency,
	}
	logger.Infof("Queue container is starting with BreakerParams = %#v", params)
	return queue.NewBreaker(params)
}

func supportsMetrics(ctx context.Context, logger *zap.SugaredLogger, env config) bool {
	// Setup request metrics reporting for end-user metrics.
	if env.ServingRequestMetricsBackend == "" {
		return false
	}
	if err := setupMetricsExporter(ctx, logger, env.ServingRequestMetricsBackend, env.ServingRequestMetricsReportingPeriodSeconds, env.MetricsCollectorAddress); err != nil {
		logger.Errorw("Error setting up request metrics exporter. Request metrics will be unavailable.", zap.Error(err))
		return false
	}

	return true
}

func requestLogHandler(logger *zap.SugaredLogger, currentHandler http.Handler, env config) http.Handler {
	revInfo := &pkghttp.RequestLogRevision{
		Name:          env.ServingRevision,
		Namespace:     env.ServingNamespace,
		Service:       env.ServingService,
		Configuration: env.ServingConfiguration,
		PodName:       env.ServingPod,
		PodIP:         env.ServingPodIP,
	}
	handler, err := pkghttp.NewRequestLogHandler(currentHandler, logging.NewSyncFileWriter(os.Stdout), env.ServingRequestLogTemplate,
		pkghttp.RequestLogTemplateInputGetterFromRevision(revInfo), env.ServingEnableProbeRequestLog)
	if err != nil {
		logger.Errorw("Error setting up request logger. Request logs will be unavailable.", zap.Error(err))
		return currentHandler
	}
	return handler
}

func requestMetricsHandler(logger *zap.SugaredLogger, currentHandler http.Handler, env config) http.Handler {
	h, err := queue.NewRequestMetricsHandler(currentHandler, env.ServingNamespace,
		env.ServingService, env.ServingConfiguration, env.ServingRevision, env.ServingPod)
	if err != nil {
		logger.Errorw("Error setting up request metrics reporter. Request metrics will be unavailable.", zap.Error(err))
		return currentHandler
	}
	return h
}

func requestAppMetricsHandler(logger *zap.SugaredLogger, currentHandler http.Handler, breaker *queue.Breaker, env config) http.Handler {
	h, err := queue.NewAppRequestMetricsHandler(currentHandler, breaker, env.ServingNamespace,
		env.ServingService, env.ServingConfiguration, env.ServingRevision, env.ServingPod)
	if err != nil {
		logger.Errorw("Error setting up app request metrics reporter. Request metrics will be unavailable.", zap.Error(err))
		return currentHandler
	}
	return h
}

func setupMetricsExporter(ctx context.Context, logger *zap.SugaredLogger, backend string, reportingPeriod int, collectorAddress string) error {
	// Set up OpenCensus exporter.
	// NOTE: We use revision as the component instead of queue because queue is
	// implementation specific. The current metrics are request relative. Using
	// revision is reasonable.
	// TODO(yanweiguo): add the ability to emit metrics with names not combined
	// to component.
	ops := metrics.ExporterOptions{
		Domain:         metrics.Domain(),
		Component:      "revision",
		PrometheusPort: networking.UserQueueMetricsPort,
		ConfigMap: map[string]string{
			metrics.BackendDestinationKey:      backend,
			"metrics.opencensus-address":       collectorAddress,
			"metrics.reporting-period-seconds": strconv.Itoa(reportingPeriod),
		},
	}
	return metrics.UpdateExporter(ctx, ops, logger)
}

func flush(logger *zap.SugaredLogger) {
	logger.Sync()
	os.Stdout.Sync()
	os.Stderr.Sync()
	metrics.FlushExporter()
}
