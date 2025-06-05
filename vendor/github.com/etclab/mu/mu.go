// Package mu (mini utility) contains miscellaneous utility functions that are
// generally useful for most projects.
package mu

import (
	"fmt"
	"os"
	"strings"
)

// Fatalf writes a message to stderr, and then calls [os.Exit](1).
// Arguments are handled in the manner of [fmt.Fprintf].
func Fatalf(format string, a ...any) {
	if !strings.HasSuffix(format, "\n") {
		format += "\n"
	}
	fmt.Fprintf(os.Stderr, format, a...)
	os.Exit(1)
}

// Panicf invokes panic() with a message.
// Arguments are handled in the manner of [fmt.Sprintf].
func Panicf(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	panic(msg)
}

// BUG simply invoked [Panicf], but prepends the message with "bug: "
func BUG(format string, a ...any) {
	Panicf("bug: "+format, a...)
}

// UNUSED is a noop function that is primarily used during development to
// silence build errors of the form "variable x declared and not used."
// UNUSED takes a variable number of arguments, and thus may silence such
// errors for multiple variables with a single call.
//
// Example:
//
//	a, b, err = foo()
//	if err != nil {
//	    mu.Fatalf("error: %v", err)
//	}
//	mu.UNUSED(a, b)
func UNUSED(v ...any) {}

// BoolToInt converts a bool to int.  It returns 1 if b is true and 0 if b is
// false.
func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
