package mutil

import "os"

type FunctionMode string

const (
	FunctionModeSingle     FunctionMode = "SINGLE"
	FunctionModeChain      FunctionMode = "CHAIN"
	FunctionModeMacroChain FunctionMode = "MACRO_CHAIN" // chain for macro-bench
	FunctionModeFake       FunctionMode = "FAKE"
	FunctionModeEmpty      FunctionMode = ""
)

func GetFunctionMode() FunctionMode {
	return FunctionMode(os.Getenv("FUNCTION_MODE"))
}
