// Package main provides a tool for generating Ed25519 key pairs and signed
// genesis hashes for an append-only log mechanism.
//
// Append-Only Log Client Key Generator
//
// This tool generates cryptographic materials for a trusted client in an
// append-only log system backed by etcd:
//   - Ed25519 key pair for signing (client-pk.pem, client-sk.pem)
//   - SHA256 genesis hash from random bytes (<name>.hash)
//   - Ed25519 signature of the hash (<name>.hash.sig)
//
// Usage:
//
//	go run ./dev/client/ -name genesis              # Generate keys + one hash
//	go run ./dev/client/ -name block1 -name block2  # Multiple hashes
//	go run ./dev/client/ -name genesis -newkeys     # Force new keys
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	privateKeyFile = "client-sk.pem"
	publicKeyFile  = "client-pk.pem"
)

// stringSlice implements flag.Value for collecting multiple -name flags
type stringSlice []string

func (s *stringSlice) String() string {
	return fmt.Sprintf("%v", *s)
}

func (s *stringSlice) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func main() {
	var names stringSlice
	var newKeys bool

	flag.Var(&names, "name", "Name for the hash file (can be specified multiple times)")
	flag.BoolVar(&newKeys, "newkeys", false, "Force regeneration of key pair")
	flag.Parse()

	if len(names) == 0 {
		fmt.Println("Usage: go run main.go -name <hashname> [-name <hashname2>...] [-newkeys]")
		fmt.Println("  -name string    Name for the hash file (required, can be repeated)")
		fmt.Println("  -newkeys        Force regeneration of key pair")
		os.Exit(1)
	}

	// Get the directory where this program is located
	execDir, err := getExecDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error getting executable directory: %v\n", err)
		os.Exit(1)
	}

	// Generate or load key pair
	privateKey, err := getOrCreateKeyPair(execDir, newKeys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error with key pair: %v\n", err)
		os.Exit(1)
	}

	// Generate hash and signature for each name
	for _, name := range names {
		if err := generateHashAndSignature(execDir, name, privateKey); err != nil {
			fmt.Fprintf(os.Stderr, "Error generating hash/signature for %q: %v\n", name, err)
			os.Exit(1)
		}
		fmt.Printf("Generated %s.hash and %s.hash.sig\n", name, name)
	}

	fmt.Println("Done!")
}

// getExecDir returns the directory containing the source file
func getExecDir() (string, error) {
	// When running with `go run`, use current working directory
	// or the directory of the source file
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	// Check if we're in the dev/client directory or need to navigate there
	if filepath.Base(wd) == "client" {
		return wd, nil
	}

	// Check if dev/client exists relative to working directory
	clientDir := filepath.Join(wd, "dev", "client")
	if _, err := os.Stat(clientDir); err == nil {
		return clientDir, nil
	}

	return wd, nil
}

// getOrCreateKeyPair loads existing keys or generates new ones
func getOrCreateKeyPair(dir string, forceNew bool) (ed25519.PrivateKey, error) {
	skPath := filepath.Join(dir, privateKeyFile)
	pkPath := filepath.Join(dir, publicKeyFile)

	// Check if keys exist and we're not forcing regeneration
	if !forceNew {
		if _, err := os.Stat(skPath); err == nil {
			fmt.Println("Loading existing key pair...")
			return loadPrivateKey(skPath)
		}
	}

	// Generate new key pair
	fmt.Println("Generating new Ed25519 key pair...")
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	// Save private key
	if err := savePrivateKey(skPath, privateKey); err != nil {
		return nil, fmt.Errorf("failed to save private key: %w", err)
	}
	fmt.Printf("Saved private key to %s\n", privateKeyFile)

	// Save public key
	if err := savePublicKey(pkPath, publicKey); err != nil {
		return nil, fmt.Errorf("failed to save public key: %w", err)
	}
	fmt.Printf("Saved public key to %s\n", publicKeyFile)

	return privateKey, nil
}

// savePrivateKey saves an Ed25519 private key in PEM format
func savePrivateKey(path string, key ed25519.PrivateKey) error {
	pkcs8Key, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: pkcs8Key,
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create private key file: %w", err)
	}
	defer file.Close()

	return pem.Encode(file, block)
}

// savePublicKey saves an Ed25519 public key in PEM format
func savePublicKey(path string, key ed25519.PublicKey) error {
	pkixKey, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal public key: %w", err)
	}

	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pkixKey,
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to create public key file: %w", err)
	}
	defer file.Close()

	return pem.Encode(file, block)
}

// loadPrivateKey loads an Ed25519 private key from a PEM file
func loadPrivateKey(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("failed to decode PEM block")
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

// generateHashAndSignature generates a random genesis hash and signs it
func generateHashAndSignature(dir, name string, privateKey ed25519.PrivateKey) error {
	// Generate 32 cryptographically random bytes
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Compute SHA256 hash
	hash := sha256.Sum256(randomBytes)

	// Sign the hash
	signature := ed25519.Sign(privateKey, hash[:])

	// Save hash (hex-encoded)
	hashPath := filepath.Join(dir, name+".hash")
	hashHex := hex.EncodeToString(hash[:])
	if err := os.WriteFile(hashPath, []byte(hashHex), 0644); err != nil {
		return fmt.Errorf("failed to write hash file: %w", err)
	}

	// Save signature (hex-encoded)
	sigPath := filepath.Join(dir, name+".hash.sig")
	sigHex := hex.EncodeToString(signature)
	if err := os.WriteFile(sigPath, []byte(sigHex), 0644); err != nil {
		return fmt.Errorf("failed to write signature file: %w", err)
	}

	return nil
}
