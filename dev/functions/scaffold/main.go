package main

import (
	"fmt"
	"os"

	f "function"

	ce "knative.dev/func-go/cloudevents"
)

// const (
// 	attestedHttpsListenAddress = "127.0.0.1:8443"
// )

func main() {
	// targetAddr := ce.DefaultListenAddress

	// go startTLSProxy(targetAddr)

	if err := ce.Start(ce.DefaultHandler{Handler: f.Handle}); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

// func LogWithPrefix(prefix string) func(format string, v ...interface{}) {
// 	return func(format string, v ...interface{}) {
// 		log.Printf("["+prefix+"] "+format, v...)
// 	}
// }

// func startTLSProxy(targetAddr string) {
// 	logDev := LogWithPrefix("dev - startTLSProxy")

// 	targetURL, err := url.Parse("http://" + targetAddr)
// 	if err != nil {
// 		logDev("failed to parse internal target URL: %v", err)
// 		os.Exit(1)
// 	}

// 	// forward requests to the function service
// 	proxy := httputil.NewSingleHostReverseProxy(targetURL)

// 	// TLS config for the proxy's public-facing listener
// 	tlsCfg, err := enclave.CreateAttestationServerTLSConfig()
// 	if err != nil {
// 		logDev("failed to create attestation TLS config: %v\n", err)
// 		os.Exit(1)
// 	}

// 	// configure the HTTPS server for the proxy
// 	proxyServer := &http.Server{
// 		Addr:         attestedHttpsListenAddress,
// 		Handler:      proxy,
// 		TLSConfig:    tlsCfg,
// 		ReadTimeout:  5 * time.Second,   // Protects against slow clients sending headers
// 		WriteTimeout: 10 * time.Second,  // Max time to write a response
// 		IdleTimeout:  120 * time.Second, // Important for keep-alives
// 	}

// 	logDev("Reverse proxy starting on %s\n", attestedHttpsListenAddress)
// 	log.Fatal(proxyServer.ListenAndServeTLS("", ""))
// }
