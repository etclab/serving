package main

import (
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	f "function"

	"github.com/edgelesssys/ego/enclave"
	ce "knative.dev/func-go/cloudevents"
)

const (
	attestedHttpsListenAddress = "127.0.0.1:8443"
)

func main() {
	targetAddr := ce.DefaultListenAddress

	go startTLSProxy(targetAddr)

	if err := ce.Start(ce.DefaultHandler{Handler: f.Handle}); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}

func LogWithPrefix(prefix string) func(format string, v ...interface{}) {
	return func(format string, v ...interface{}) {
		log.Printf("["+prefix+"] "+format, v...)
	}
}

func startTLSProxy(targetAddr string) {
	logDev := LogWithPrefix("dev - startTLSProxy")

	targetURL, err := url.Parse("http://" + targetAddr)
	if err != nil {
		logDev("failed to parse internal target URL: %v", err)
		os.Exit(1)
	}

	// forward requests to the function service
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// TLS config for the proxy's public-facing listener
	tlsCfg, err := enclave.CreateAttestationServerTLSConfig()
	if err != nil {
		logDev("failed to create attestation TLS config: %v\n", err)
		os.Exit(1)
	}

	// configure the HTTPS server for the proxy
	proxyServer := &http.Server{
		Addr:      attestedHttpsListenAddress,
		Handler:   proxy,
		TLSConfig: tlsCfg,
	}

	logDev("Reverse proxy starting on %s\n", attestedHttpsListenAddress)
	log.Fatal(proxyServer.ListenAndServeTLS("", ""))
}

// // 1. Define the target URL for the backend function service.
// targetURL, err := url.Parse("http://localhost:8080")
// if err != nil {
// 	log.Fatal("Invalid target URL")
// }

// // 2. Create a reverse proxy.
// proxy := httputil.NewSingleHostReverseProxy(targetURL)

// // 3. Create the TLS config for the proxy's public-facing listener.
// tlsCfg, err := enclave.CreateAttestationServerTLSConfig()
// if err != nil {
// 	fmt.Fprintf(os.Stderr, "failed to create attestation TLS config: %v\n", err)
// 	os.Exit(1)
// }

// // 4. Create and configure the HTTPS server for the proxy.
// proxyServer := &http.Server{
// 	Addr:      ":8443", // The public-facing HTTPS port
// 	Handler:   proxy,
// 	TLSConfig: tlsCfg,
// }

// fmt.Println("Starting reverse proxy on https://localhost:8443")
// fmt.Printf("Proxying requests to %s\n", targetURL)

// // 5. Start the proxy server.
// // ListenAndServeTLS will use the certificates from the TLSConfig.
// if err := proxyServer.ListenAndServeTLS("", ""); err != nil {
// 	log.Fatalf("Proxy server failed: %v", err)
// }
