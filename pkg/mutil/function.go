package mutil

import "os"

type FunctionMode string

const (
	FunctionModeSingle FunctionMode = "SINGLE"
	FunctionModeChain  FunctionMode = "CHAIN"
	FunctionModeEmpty  FunctionMode = ""
)

func GetFunctionMode() FunctionMode {
	return FunctionMode(os.Getenv("FUNCTION_MODE"))
}
