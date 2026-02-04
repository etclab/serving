/*
Copyright 2024 The Knative Authors

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

package main

import (
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/etclab/pre"
	"github.com/google/uuid"
	vegeta "github.com/tsenart/vegeta/v12/lib"
	"knative.dev/pkg/injection"
	"knative.dev/pkg/signals"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"
	"knative.dev/serving/test/performance/performance"
)

// First function in the chain
const targetName = "validate-fun"

var (
	target   = flag.String("target", "broker-ingress", "The target to attack (broker-ingress or direct)")
	duration = flag.Duration("duration", 5*time.Minute, "The duration of the benchmark")
)

// emojiShortcodes is a sample of emoji shortcodes from the pool
var emojiShortcodes = []string{
	":joy:",
	":sunglasses:",
	":doughnut:",
	":stuck_out_tongue_winking_eye:",
	":money_mouth_face:",
	":flushed:",
	":mask:",
	":nerd_face:",
	":ghost:",
	":skull_and_crossbones:",
	":heart_eyes_cat:",
	":hear_no_evil:",
	":see_no_evil:",
	":speak_no_evil:",
	":boy:",
	":girl:",
	":man:",
	":woman:",
	":older_man:",
	":policeman:",
	":guardsman:",
	":construction_worker_man:",
	":prince:",
	":princess:",
	":man_in_tuxedo:",
	":bride_with_veil:",
	":mrs_claus:",
	":santa:",
	":turkey:",
	":rabbit:",
	":no_good_woman:",
	":ok_woman:",
	":raising_hand_woman:",
	":bowing_man:",
	":man_facepalming:",
	":woman_shrugging:",
	":massage_woman:",
	":walking_man:",
	":running_man:",
	":dancer:",
	":man_dancing:",
	":dancing_women:",
	":rainbow:",
	":skier:",
	":golfing_man:",
	":surfing_man:",
	":basketball_man:",
	":biking_man:",
	":point_up_2:",
	":vulcan_salute:",
	":metal:",
	":call_me_hand:",
	":thumbsup:",
	":wave:",
	":clap:",
	":raised_hands:",
	":pray:",
	":dog:",
	":cat2:",
	":pig:",
	":hatching_chick:",
	":snail:",
	":bacon:",
	":pizza:",
	":taco:",
	":burrito:",
	":ramen:",
	":champagne:",
	":tropical_drink:",
	":beer:",
	":tumbler_glass:",
	":world_map:",
	":beach_umbrella:",
	":mountain_snow:",
	":camping:",
	":steam_locomotive:",
	":flight_departure:",
	":rocket:",
	":star2:",
	":sun_behind_small_cloud:",
	":cloud_with_rain:",
	":fire:",
	":jack_o_lantern:",
	":balloon:",
	":tada:",
	":trophy:",
	":iphone:",
	":pager:",
	":fax:",
	":bulb:",
	":money_with_wings:",
	":crystal_ball:",
	":underage:",
	":interrobang:",
	":100:",
	":checkered_flag:",
	":crossed_swords:",
	":floppy_disk:",
	":poop:",
}

// getRandomShortcode returns a random emoji shortcode from the pool
func getRandomShortcode() string {
	return emojiShortcodes[rand.Intn(len(emojiShortcodes))]
}

// Encryption cache and one-time initialization
var (
	encryptionCache   = make(map[[32]byte][]byte)
	encryptionCacheMu sync.RWMutex
	logOnce           sync.Once

	// Cached parsed keys (parsed once)
	rsaKeyOnce    sync.Once
	rsaKey        *rsa.PrivateKey
	rsaKeyMissing bool

	preKeysOnce    sync.Once
	prePP          *pre.PublicParams
	prePK          *pre.PublicKey
	preKeysMissing bool
)

// getEncryptedMessage encrypts the message based on STRATEGY environment variable
// Strategies:
// - knative: no encryption
// - efunction: no encryption
// - rsa-efunction: RSA hybrid encryption
// - member-efunction: PRE encryption
// - leader-efunction: PRE encryption
// Results are cached based on the SHA256 hash of msgBytes to avoid re-encryption.
func getEncryptedMessage(msgBytes []byte) []byte {
	strategy := os.Getenv("STRATEGY")

	// Log strategy only once
	logOnce.Do(func() {
		log.Printf("Strategy: %s", strategy)
	})

	// For strategies without encryption, return as-is (no caching needed)
	switch strategy {
	case "knative", "efunction", "":
		logOnce.Do(func() {
			log.Printf("No encryption (strategy: %s)", strategy)
		})
		return msgBytes
	}

	// Compute hash for cache lookup
	hash := sha256.Sum256(msgBytes)

	// Check cache first
	encryptionCacheMu.RLock()
	if cached, ok := encryptionCache[hash]; ok {
		encryptionCacheMu.RUnlock()
		return cached
	}
	encryptionCacheMu.RUnlock()

	// Encrypt based on strategy
	var encryptedBytes []byte

	switch strategy {
	case "rsa-efunction":
		// Parse RSA key once
		rsaKeyOnce.Do(func() {
			rsaKeyStr := os.Getenv("RSA_SK")
			if rsaKeyStr == "" {
				log.Printf("RSA_SK is not set, returning plain message")
				rsaKeyMissing = true
				return
			}
			var err error
			rsaKey, err = mutil.UnmarshalRSAPrivateKeyFromPEM([]byte(rsaKeyStr))
			if err != nil {
				log.Fatalf("failed to parse RSA private key: %v", err.Error())
			}
			log.Printf("RSA key parsed successfully")
		})

		if rsaKeyMissing {
			return msgBytes
		}

		var err error
		encryptedBytes, err = mutil.RSAHybridEncrypt(&rsaKey.PublicKey, msgBytes)
		if err != nil {
			log.Fatalf("failed to encrypt message using RSA: %v", err.Error())
		}

	case "member-efunction", "leader-efunction", "both", "both-sig", "both-hash-chain-sig":
		// Parse PRE keys once
		preKeysOnce.Do(func() {
			pps := os.Getenv("LEADER_PP")
			pks := os.Getenv("LEADER_PK")

			if pps == "" || pks == "" {
				log.Printf("LEADER_PP or LEADER_PK not set, returning plain message")
				preKeysMissing = true
				return
			}

			var err error
			prePP, err = samba.ParsePublicParams([]byte(pps))
			if err != nil {
				log.Fatalf("failed to parse public params: %v", err.Error())
			}
			prePK, err = samba.ParsePublicKey([]byte(pks))
			if err != nil {
				log.Fatalf("failed to parse public key: %v", err.Error())
			}
			log.Printf("PRE keys parsed successfully")
		})

		if preKeysMissing {
			return msgBytes
		}

		var err error
		encryptedBytes, err = mutil.PreEncrypt(prePP, prePK, msgBytes, targetName, []byte{})
		if err != nil {
			log.Fatalf("failed to encrypt message: %v", err.Error())
		}

	default:
		log.Printf("Unknown strategy: %s, returning plain message", strategy)
		return msgBytes
	}

	// Store in cache
	encryptionCacheMu.Lock()
	encryptionCache[hash] = encryptedBytes
	encryptionCacheMu.Unlock()

	return encryptedBytes
}

// getCloudEventHeaders returns CloudEvents headers for the request
func getCloudEventHeaders() http.Header {
	headers := http.Header{
		"Content-Type":     []string{"application/json"},
		"Ce-Id":            []string{uuid.New().String()},
		"Ce-Specversion":   []string{"1.0"},
		"Ce-Type":          []string{"dev.knative.sources.ping"},
		"Ce-Source":        []string{"/apis/v1/namespaces/default/pingsources/ping-source"},
		"Ce-Aggsignature":  []string{""},
		"Ce-Functionchain": []string{""},
	}

	// Add nonce for signature aggregation if enabled
	attachSignature := os.Getenv("ATTACH_SIGNATURE")
	if attachSignature == "true" {
		logOnce.Do(func() {
			log.Printf("ATTACH_SIGNATURE is true, adding nonce to header")
		})
		headers.Set("Ce-Nonce", fmt.Sprintf("%d", time.Now().Unix()))
	}

	return headers
}

// brokerURL is the Kafka broker ingress URL
const brokerURL = "http://broker-ingress.knative-eventing.svc.cluster.local/default/broker"

// directURL is the direct function invocation URL
const directURL = "http://validate-fun.default.svc.cluster.local"

// getBrokerTargeter returns a vegeta targeter for the Kafka broker ingress
// that randomly selects emoji shortcodes for each request
func getBrokerTargeter() vegeta.Targeter {
	return func(t *vegeta.Target) error {
		shortcode := getRandomShortcode()
		plainBody := []byte(fmt.Sprintf(`{"shortcode":"%s"}`, shortcode))

		t.Method = http.MethodPost
		t.URL = brokerURL
		t.Body = getEncryptedMessage(plainBody)
		t.Header = getCloudEventHeaders()
		return nil
	}
}

// getDirectTargeter returns a vegeta targeter for direct function invocation
// that randomly selects emoji shortcodes for each request
func getDirectTargeter() vegeta.Targeter {
	return func(t *vegeta.Target) error {
		shortcode := getRandomShortcode()
		plainBody := []byte(fmt.Sprintf(`{"shortcode":"%s"}`, shortcode))

		t.Method = http.MethodPost
		t.URL = directURL
		t.Body = getEncryptedMessage(plainBody)
		t.Header = getCloudEventHeaders()
		return nil
	}
}

func main() {
	ctx := signals.NewContext()
	cfg := injection.ParseAndGetRESTConfigOrDie()
	ctx, _ = injection.EnableInjectionOrDie(ctx, cfg)

	if *target == "" {
		log.Fatalf("-target is a required flag")
	}

	log.Println("Starting func-chain benchmark")
	log.Printf("Target: %s", *target)
	log.Printf("Duration: %s", *duration)

	ctx, cancel := context.WithTimeout(ctx, *duration+time.Minute)
	defer cancel()

	// Select targeter and URL based on flag
	var targeter vegeta.Targeter
	var targetURL string
	switch *target {
	case "broker-ingress":
		targeter = getBrokerTargeter()
		targetURL = brokerURL
	case "direct":
		targeter = getDirectTargeter()
		targetURL = directURL
	default:
		log.Fatalf("Unrecognized target: %s (use 'broker-ingress' or 'direct')", *target)
	}

	// Wait for target to be ready
	if err := performance.ProbeTargetTillReady(targetURL, *duration); err != nil {
		log.Fatalf("Failed to get target ready: %v", err)
	}

	// Get rate from environment variable
	freq, err := strconv.Atoi(os.Getenv("RATE"))
	if err != nil || freq <= 0 {
		freq = 100 // Default rate
	}
	log.Printf("Rate: %d req/sec", freq)
	log.Printf("Using %d emoji shortcodes for random selection", len(emojiShortcodes))

	rate := vegeta.Rate{Freq: freq, Per: time.Second}
	attacker := vegeta.NewAttacker(vegeta.Timeout(180*time.Second), vegeta.MaxWorkers(100))

	// Start the attack
	log.Println("Starting load test...")
	results := attacker.Attack(targeter, rate, *duration, "func-chain-load-test")

	metricResults := &vegeta.Metrics{}

LOOP:
	for {
		select {
		case <-ctx.Done():
			break LOOP
		case res, ok := <-results:
			if ok {
				metricResults.Add(res)
			} else {
				break LOOP
			}
		}
	}

	// Compute and report results
	metricResults.Close()
	_ = vegeta.NewTextReporter(metricResults).Report(os.Stdout)

	log.Println("Func-chain benchmark completed")
}
