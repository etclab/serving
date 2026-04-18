package cryptofun

import (
	"crypto/rand"
	"os"
	"testing"

	"github.com/etclab/mu"
)

var OneKiBMessage []byte

func TestMain(m *testing.M) {
	OneKiBMessage = make([]byte, 1024)
	_, err := rand.Read(OneKiBMessage)
	if err != nil {
		mu.Fatalf("rand.Read failed: %v", err)
	}
	status := m.Run()
	os.Exit(status)
}
