package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	signaltransport "github.com/jtarchie/secret-agent/internal/chat/signal"
)

// SignalLinkCmd QR-links a Signal secondary device.
type SignalLinkCmd struct {
	StateDir   string `help:"directory for signal-cli state (will be created if missing)"         name:"signal-state-dir"                        required:""`
	DeviceName string `default:"secret-agent"                                                     help:"device name shown on the primary device" name:"signal-device-name"`
	SignalCLI  string `default:"signal-cli"                                                       help:"path to the signal-cli binary"           name:"signal-cli"`
	NoQR       bool   `help:"do not render a QR code in the terminal; only print the linking URI" name:"no-qr"`
	Verbose    int    `help:"verbosity: 0 info, 1 debug"`
}

func (c *SignalLinkCmd) Run() error {
	err := os.MkdirAll(c.StateDir, 0o700)
	if err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if c.NoQR {
		fmt.Fprintln(os.Stderr, "Scan the URI below as a QR code in your Signal primary device (Linked devices → Link new device):")
	} else {
		fmt.Fprintln(os.Stderr, "Scan the QR code below with your Signal primary device (Linked devices → Link new device). The raw URI is printed first as a fallback.")
	}
	number, err := signaltransport.Link(ctx, signaltransport.LinkConfig{
		Command:    c.SignalCLI,
		StateDir:   c.StateDir,
		DeviceName: c.DeviceName,
		NoQRCode:   c.NoQR,
		Logger:     newLogger(c.Verbose),
	})
	if err != nil {
		return fmt.Errorf("signal link: %w", err)
	}
	fmt.Fprintf(os.Stderr, "linked account: %s\n", number)
	return nil
}
