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
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	bls "github.com/cloudflare/circl/ecc/bls12381"
	"github.com/etclab/ncircl/aggsig/bgls03"
	"github.com/etclab/pre"
	injection "knative.dev/pkg/injection"

	"github.com/edgelesssys/ego/attestation"
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

				// watch for member's public keys at prefix: members/<leader-pod-id>/publicKey/
				memberPublicKeyDir := "members/" + myId + "/publicKey"
				go d.KeyRegistry.ListWatchMemberPublicKeys(memberPublicKeyDir, myId)

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

				err = d.KeyRegistry.StorePublicKey(lPublicKeyLabel, keyPair.PK)
				if err != nil {
					// TODO: log the error and maybe retry later
					logDev("Error storing public key in KeyRegistry: %v", err)
				}

				err = d.KeyRegistry.StorePublicParams(lPublicParamsLabel, pp)
				if err != nil {
					// TODO: log the error and maybe retry later
					logDev("Error storing public params in KeyRegistry: %v", err)
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

				go d.KeyRegistry.ListWatchEveryLeaderPublicKeys("leaders/")

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

				// watch for re-encryption keys at exact prefix:
				// members/<leader-pod-id>/reEncryptionKey/<my-pod-id>
				reEncKeyDir := "members/" + leaderIdentity + "/reEncryptionKey/" + myId
				go d.KeyRegistry.ListWatchReEncryptionKey(reEncKeyDir, leaderIdentity)

				myFunctionRevision := d.KeyRegistry.FunctionId
				myService := d.KeyRegistry.ServiceName
				// leader publicKey and publicParams are at:
				// leaders/<service-name>/<function-revision>/publicKey/<leader-pod-id>
				// leaders/<service-name>/<function-revision>/publicParams/<leader-pod-id>
				leaderPublicPrefix := "leaders/" + myService + "/" + myFunctionRevision + "/public"

				// implements the List & Watch pattern
				// https://www.mgasch.com/2021/01/listwatch-part-1/#the-list--watch-pattern
				go d.KeyRegistry.ListWatchLeaderKeys(leaderPublicPrefix, leaderIdentity)
			},
		},
	})
}

func initEtcdWithRetry(d *Defaults) {
	d.KeyRegistry.InitEtcdWithRetry()
}

func Main(opts ...Option) error {
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

	// Setup the Logger.
	logger, _ := pkglogging.NewLogger(env.ServingLoggingConfig, env.ServingLoggingLevel)
	defer flush(logger)

	logDev := mutil.LogWithPrefix("dev - Main")
	logDev("d.Env = %+v", d.Env)

	d.KeyRegistry = new(kregistry.KeyRegistry)
	d.KeyRegistry.IsEtcdReady = make(chan struct{})
	d.KeyRegistry.LoadMyEnvVars(d.Env.ServingService)

	// env.ServingPod is the pod id
	go d.KeyRegistry.SetupSignature(env.AttachSignature, env.ServingPod)

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

	// is pod read for proxy re-encryption?
	// wait until this pod becomes the leader or joins as a member
	<-d.KeyRegistry.IsPodPreReady

	// THINK: with all the leader election, etcd connection with retry,
	// re-encryption key generation, and running on top of enclaves -
	// are there ways we can speed up the queue-proxy startup?
	logger.Info("Starting queue-proxy")

	errCh := make(chan error)
	for name, server := range httpServers {
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
func initKeyRegistry() Option {
	return func(d *Defaults) {
		d.KeyRegistry.InstanceId = d.Env.ServingPodIP
		d.KeyRegistry.FunctionId = d.KeyRegistry.GetFunctionId(d.Env.ServingRevision)
		d.KeyRegistry.ServiceName = d.KeyRegistry.GetServiceName(d.Env.ServingService)
		d.KeyRegistry.PodId = d.Env.ServingPod
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

	plaintext, err := mutil.RSADecrypt(d.KeyRegistry.RSASecretKey, encryptedBytes)
	if err != nil {
		logDev("failed to decrypt message using RSA private key: %v", err.Error())
		return nil, err
	}
	return plaintext, nil
}

func (d *DebugTransport) decryptSambaMessage(encryptedBytes []byte) ([]byte, error) {
	logDev := mutil.LogWithPrefix("dev - decryptSambaMessage")

	var sambaMessage *samba.SambaMessage
	if err := json.Unmarshal(encryptedBytes, &sambaMessage); err != nil {
		logDev("Invalid message format: %v", err)
		return nil, err
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
			return nil, err
		}
	}

	// decrypt the ciphertext, get the plaintext
	plaintext, err := mutil.Decrypt(myPublicParams, myKeyPair.SK, sambaMessage)
	if err != nil {
		logDev("Error decrypting message: %v", err)
		return nil, err
	}

	return plaintext, nil
}

// decrypts the response for user-container
func (d *DebugTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	logDev := mutil.LogWithPrefix("dev - RoundTrip")

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

	nonce := req.Header.Get("Ce-Nonce")
	if nonce == "" {
		logDev("Request missing Ce-Nonce header, cannot add signature.")
	} else {
		// instanceId is unique instance id (or pod id) of the function
		messageToSign := nonce + "|" + d.KeyRegistry.PodId
		logDev("Message to sign: %s", messageToSign)

		// if existing signature then parse it and update it
		existingAggSignature := req.Header.Get("Ce-Aggsignature")
		// records the function instance ids that have signed this message
		funChain := req.Header.Get("Ce-Functionchain")
		logDev("Existing signature: %s", existingAggSignature)
		logDev("Existing function chain: %s", funChain)

		// based on TestManySign() method in ncircle/aggsig/bgls03/bgls03_test.go
		// https://github.com/etclab/ncircl/blob/7667a1b1a68bbbfaab501187acb5001ddc3a8754/aggsig/bgls03/bgls03_test.go#L36-L58
		var aggSig *bgls03.Signature
		if existingAggSignature == "" {
			// first function in the chain
			aggSig = bgls03.NewSignature()
			bgls03.Sign(d.KeyRegistry.SigPp, d.KeyRegistry.SigSk, []byte(messageToSign), aggSig)
		} else {
			// if there's an existing signature, parse it and add to it
			eSigBytes, err := hex.DecodeString(existingAggSignature)
			if err != nil {
				logDev("Error decoding existing signature from hex: %v", err)
				return nil, err
			}
			g1 := new(bls.G1)
			err = g1.SetBytes(eSigBytes)
			if err != nil {
				logDev("Error parsing existing signature bytes: %v", err)
				return nil, err
			}

			aggSig = &bgls03.Signature{
				Sig: g1,
			}

			bgls03.Sign(d.KeyRegistry.SigPp, d.KeyRegistry.SigSk, []byte(messageToSign), aggSig)
		}

		sigBytes := aggSig.Sig.BytesCompressed()
		sigHex := hex.EncodeToString(sigBytes)

		// attach updated signature to the request header
		logDev("Attaching signature to request header: %s", sigHex)
		if funChain == "" {
			funChain = d.KeyRegistry.PodId
		} else {
			funChain = funChain + "|" + d.KeyRegistry.PodId
		}
		d.KeyRegistry.StoreAggSignatureAndChain(nonce, funChain, sigHex)
	}

	encBody, err := io.ReadAll(req.Body)
	if err != nil {
		logDev("Error reading request body: %v", err)
		return nil, err
	}
	req.Body.Close()
	logDev("Request Body (encrypted): %s", string(encBody))

	var plaintext []byte

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
			plaintext, err = d.decryptSambaMessage(encBody)
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
			plaintext, err = d.decryptSambaMessage(encBody)
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

	return d.Transport.RoundTrip(req)
}

func verifyReport(report attestation.Report) error {
	// You can either verify the UniqueID or the tuple (SignerID, ProductID, SecurityVersion, Debug).
	// TODO: inject the actual signer id as env in enclave.json
	signerid, err := hex.DecodeString("36de55e9a3365d9fc0890696a2bd230a9dffbc98e2bf47a029707f8e33e710c6")
	if err != nil {
		return errors.New("failed to decode signer ID")
	}

	// set to 1 in enclave.json
	if report.SecurityVersion != 1 {
		return errors.New("invalid security version")
	}
	// set to 1 in enclave.json
	if binary.LittleEndian.Uint16(report.ProductID) != 1 {
		return errors.New("invalid product")
	}
	// for now we just check if the length is equal
	// if !bytes.Equal(report.SignerID, signer) {
	if len(report.SignerID) != len(signerid) {
		return errors.New("invalid signer")
	}

	// For production, you must also verify that report.Debug == false

	return nil
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
