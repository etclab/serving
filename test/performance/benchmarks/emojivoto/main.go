/*
Copyright 2022 The Knative Authors

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
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	vegeta "github.com/tsenart/vegeta/v12/lib"
	"knative.dev/pkg/injection"
	"knative.dev/serving/pkg/mutil"
	"knative.dev/serving/pkg/samba"
	"knative.dev/serving/test/performance/performance"

	"knative.dev/pkg/signals"
)

const (
	benchmarkName = "emojivoto svc benchmark"
	targetName    = "web-svc"
)

var (
	target     = flag.String("target", "", "The target to attack.") // this will be function name
	duration   = flag.Duration("duration", 5*time.Minute, "The duration of the probe")
	minDefault = 100 * time.Millisecond
)

func getDefaultMessage() []byte {
	msgBytes := []byte(`{"id":0,"message":"Hi"}`)

	functionMode := mutil.FunctionMode(os.Getenv("FUNCTION_MODE"))
	log.Printf("Function mode: %s", functionMode)

	// if RSA_SK is set, we use it for encryption
	var err error
	var rsa_sk *rsa.PrivateKey
	rsa_sk_str := os.Getenv("RSA_SK")
	if rsa_sk_str != "" {
		rsa_sk, err = mutil.UnmarshalRSAPrivateKeyFromPEM([]byte(rsa_sk_str))
		if err != nil {
			log.Fatalf("failed to parse RSA private key: %v", err.Error())
		}
		log.Printf("RSA_SK is set, using it for encryption")

		encMsgBytes, err := mutil.RSAEncrypt(&rsa_sk.PublicKey, msgBytes)
		if err != nil {
			log.Fatalf("failed to encrypt message using RSA private key: %v", err.Error())
		}
		return encMsgBytes
	} else {
		log.Printf("RSA_SK is not set, not encrypting using RSA private key")
	}

	if functionMode == "SINGLE" {
		pps := os.Getenv("LEADER_PP")
		pks := os.Getenv("LEADER_PK")

		pp, err := samba.ParsePublicParams([]byte(pps))
		if err != nil {
			log.Fatalf("failed to parse public params: %v", err.Error())
		}
		pk, err := samba.ParsePublicKey([]byte(pks))
		if err != nil {
			log.Fatalf("failed to parse public key: %v", err.Error())
		}

		encryptedBytes, err := mutil.PreEncrypt(pp, pk, msgBytes, targetName)
		if err != nil {
			log.Fatalf("failed to get default message: %v", err.Error())
		}
		return encryptedBytes
	}

	return msgBytes
}

func encryptWithLeaderKey(plainBytes []byte) []byte {
	functionMode := mutil.FunctionMode(os.Getenv("FUNCTION_MODE"))
	log.Printf("Function mode: %s", functionMode)

	if functionMode == "CHAIN" {
		pps := os.Getenv("LEADER_PP")
		pks := os.Getenv("LEADER_PK")

		pp, err := samba.ParsePublicParams([]byte(pps))
		if err != nil {
			log.Fatalf("failed to parse public params: %v", err.Error())
		}
		pk, err := samba.ParsePublicKey([]byte(pks))
		if err != nil {
			log.Fatalf("failed to parse public key: %v", err.Error())
		}

		encryptedBytes, err := mutil.PreEncrypt(pp, pk, plainBytes, targetName)
		if err != nil {
			log.Fatalf("failed to get default message: %v", err.Error())
		}
		return encryptedBytes
	}

	log.Printf("Function mode is not CHAIN, not encrypting the message")
	return []byte{}
}

func getTargetForBroker() TargetSLA {
	targetSla := TargetSLA{
		slaMin: minDefault,
		slaMax: 110 * time.Millisecond,
	}

	plainBody := []byte(`{"shortcode":":dog:"}`)

	brokerUrl := "http://broker-ingress.knative-eventing.svc.cluster.local/default/broker"
	targetSla.target = vegeta.Target{
		Method: http.MethodPost,
		URL:    brokerUrl,
		// Body:   encryptWithLeaderKey(plainBody),
		Body: plainBody,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Ce-Id":          []string{uuid.New().String()},
			"Ce-Specversion": []string{"1.0"},
			"Ce-Type":        []string{"dev.knative.sources.ping"},
			"Ce-Source":      []string{"/apis/v1/namespaces/default/pingsources/ping-source"},
		},
	}

	return targetSla
}

func getTargetForApi(apiString string) TargetSLA {
	targetSla := TargetSLA{
		slaMin: minDefault,
		slaMax: 110 * time.Millisecond,
	}

	url := "/"
	if apiString != "" {
		url = apiString
	}
	fullUrl := fmt.Sprintf("http://web-svc.default.svc.cluster.local%s", url)

	targetSla.target = vegeta.Target{
		Method: http.MethodGet,
		URL:    fullUrl,
		Header: http.Header{
			"Content-Type":   []string{"application/json"},
			"Ce-Id":          []string{"1"},
			"Ce-Specversion": []string{"1.0"},
			"Ce-Type":        []string{"cloud-event-greeting"},
			"Ce-Source":      []string{"cloud-event-source"},
			"Connection":     []string{"close"},
		},
	}
	return targetSla
}

type TargetSLA struct {
	target vegeta.Target
	slaMin time.Duration
	slaMax time.Duration
}

// Map the above to our benchmark targets.
var targets = map[string]TargetSLA{
	// target name is equal to the function (image name) being invoked
	// we always fix the activator in front of the function
	"web-svc":                 getTargetForApi("/"),
	"web-svc-api-list":        getTargetForApi("/api/list"),
	"web-svc-api-vote":        getTargetForApi(fmt.Sprintf("/api/vote?choice=%s", url.QueryEscape(":metal:"))),
	"web-svc-api-leaderboard": getTargetForApi("/api/leaderboard"),
	"broker-ingress":          getTargetForBroker(),
}

func main() {
	ctx := signals.NewContext()
	cfg := injection.ParseAndGetRESTConfigOrDie()
	ctx, _ = injection.EnableInjectionOrDie(ctx, cfg)
	// startInformers()

	if *target == "" {
		log.Fatalf("-target is a required flag.")
	}

	log.Println("Starting func invocation probe for target:", *target)
	log.Printf("Running for duration %s", *duration)

	ctx, cancel := context.WithTimeout(ctx, *duration+time.Minute)
	defer cancel()

	// Based on the "target" flag, load up our target benchmark.
	// We only run one variation per run to avoid the runs being noisy neighbors,
	// which in early iterations of the benchmark resulted in latency bleeding
	// across the different workload types.
	t, ok := targets[*target]
	if !ok {
		log.Fatalf("Unrecognized target: %s", *target)
	}

	// Make sure the target is ready before sending the large amount of requests.
	if err := performance.ProbeTargetTillReady(t.target.URL, *duration); err != nil {
		log.Fatalf("Failed to get target ready for attacking: %v", err)
	}

	// Send 1000 QPS (1 per ms) for the given duration with a 30s request timeout.
	freq, err := strconv.Atoi(os.Getenv("RATE"))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
	}
	if freq <= 0 {
		freq = 1000
	}
	log.Printf("Using rate: %d\n", freq)
	rate := vegeta.Rate{Freq: freq, Per: time.Second}
	// rate := vegeta.Rate{Freq: 1, Per: time.Second}

	targeter := vegeta.NewStaticTargeter(t.target)
	// NOTE: enable client to print the response body for debugging
	// client := &http.Client{
	// 	Transport: &http.Transport{
	// 		Proxy: http.ProxyFromEnvironment,
	// 		DialContext: (&net.Dialer{
	// 			Timeout:   1 * time.Second, // very short connection timeout
	// 			KeepAlive: 30 * time.Second,
	// 		}).DialContext,
	// 		MaxIdleConns:          1000,
	// 		IdleConnTimeout:       90 * time.Second,
	// 		TLSHandshakeTimeout:   10 * time.Second,
	// 		ExpectContinueTimeout: 1 * time.Second,
	// 	},
	// }
	// attacker := vegeta.NewAttacker(vegeta.Timeout(30*time.Second), vegeta.Client(client))
	attacker := vegeta.NewAttacker(vegeta.Timeout(30 * time.Second))

	// influxReporter, err := performance.NewInfluxReporter(map[string]string{"target": *target})
	// if err != nil {
	// 	log.Fatalf("failed to create influx reporter: %v", err.Error())
	// }
	// defer influxReporter.FlushAndShutdown()

	// Start the attack!
	results := attacker.Attack(targeter, rate, *duration, "load-test")
	// deploymentStatus := performance.FetchDeploymentStatus(ctx, system.Namespace(), "activator", time.Second)

	metricResults := &vegeta.Metrics{}

LOOP:
	for {
		select {
		case <-ctx.Done():
			// If we time out or the pod gets shutdown via SIGTERM then start to
			// clean thing up.
			break LOOP

		// case ds := <-deploymentStatus:
		// 	// Report number of ready activators.
		// 	// should be always one pod for this test
		// 	influxReporter.AddDataPoint(benchmarkName, map[string]interface{}{"activator-pod-count": ds.ReadyReplicas})

		case res, ok := <-results:
			if ok {
				// log.Printf("Received body: %s", string(res.Body))
				metricResults.Add(res)
			} else {
				// If there are no more results, then we're done!
				break LOOP
			}
		}
	}

	// Compute latency percentiles
	metricResults.Close()

	// Report the results
	// influxReporter.AddDataPointsForMetrics(metricResults, benchmarkName)
	_ = vegeta.NewTextReporter(metricResults).Report(os.Stdout)

	// if err := checkSLA(metricResults, t.slaMin, t.slaMax, rate, *duration); err != nil {
	// 	// make sure to still write the stats
	// 	// influxReporter.FlushAndShutdown()
	// 	log.Fatal(err)
	// }

	log.Println("Dataplane probe test finished")
}

// func checkSLA(results *vegeta.Metrics, slaMin time.Duration, slaMax time.Duration, rate vegeta.ConstantPacer, duration time.Duration) error {
// 	// SLA 1: The p95 latency hitting the target has to be between the range defined
// 	// in the target map on top.
// 	if results.Latencies.P95 >= slaMin && results.Latencies.P95 <= slaMax {
// 		log.Printf("SLA 1 passed. P95 latency is in %d-%dms time range", slaMin, slaMax)
// 	} else {
// 		return fmt.Errorf("SLA 1 failed. P95 latency is not in %d-%dms time range: %s", slaMin, slaMax, results.Latencies.P95)
// 	}

// 	// SLA 2: making sure the defined total request is met
// 	if results.Requests == uint64(rate.Rate(time.Second)*duration.Seconds()) {
// 		log.Printf("SLA 2 passed. vegeta total request is %d", results.Requests)
// 	} else {
// 		return fmt.Errorf("SLA 2 failed. vegeta total request is %d, expected total request is %f", results.Requests, rate.Rate(time.Second)*duration.Seconds())
// 	}

// 	return nil
// }
