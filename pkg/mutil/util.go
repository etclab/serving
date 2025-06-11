package mutil

import "log"

func LogWithPrefix(prefix string) func(format string, v ...interface{}) {
	return func(format string, v ...interface{}) {
		log.Printf("["+prefix+"] "+format, v...)
	}
}
