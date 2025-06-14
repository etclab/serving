package mutil

import (
	"log"
	"sync"

	"github.com/etclab/pre"
)

func LogWithPrefix(prefix string) func(format string, v ...interface{}) {
	return func(format string, v ...interface{}) {
		log.Printf("["+prefix+"] "+format, v...)
	}
}

type PreKeys interface {
	pre.PublicKey | pre.PublicParams | pre.ReEncryptionKey | pre.KeyPair
}

func GSafeWriteToMap[K comparable, V PreKeys](key K, value *V, gMap map[K]*V, lock *sync.RWMutex) *V {
	lock.Lock()
	defer lock.Unlock()
	if gMap == nil {
		gMap = make(map[K]*V)
	}
	gMap[key] = value
	return value
}

func GSafeReadFromMap[K comparable, V PreKeys](key K, gMap map[K]*V, lock *sync.RWMutex) *V {
	lock.RLock()
	defer lock.Unlock()
	return gMap[key]
}
