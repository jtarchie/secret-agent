package main

import (
	"fmt"
	"os"

	"github.com/alecthomas/kong"
)

// CLI is the top-level Kong grammar for secret-agent.
type CLI struct {
	Run          RunCmd          `cmd:"" help:"run bots over configured transports"`
	Eval         EvalCmd         `cmd:"" help:"run a bot's tests: block as an offline eval"`
	SignalLink   SignalLinkCmd   `cmd:"" help:"QR-link a Signal secondary device"           name:"signal-link"`
	ListBuiltins ListBuiltinsCmd `cmd:"" help:"list built-in sub-agents embedded in the binary" name:"list-builtins"`
}

func main() {
	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("secret-agent"),
		kong.Description("YAML-defined chat bot with pluggable transports."),
		kong.UsageOnError(),
		kong.Exit(func(code int) {
			if code != 0 {
				code = 1
			}
			os.Exit(code)
		}),
	)
	err := ctx.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
