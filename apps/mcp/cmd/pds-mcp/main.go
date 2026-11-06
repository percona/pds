// Command pds-mcp connects an AI agent to the Percona Database Service control plane.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/percona/pds/apps/mcp/internal/cli"
)

// version is the placeholder a source build reports. The release stamps the real one with
// -ldflags "-X main.version=<version>".
var version = "0.0.0-dev"

func main() {
	os.Exit(run())
}

func run() int {
	ctx := context.Background()
	workDir, _ := os.Getwd()

	return cli.Run(ctx, cli.Env{
		Stdin:       os.Stdin,
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Getenv:      os.Getenv,
		Version:     version,
		WorkDir:     workDir,
		Interactive: isTerminal(os.Stdin),
		OpenBrowser: func(address string) error { return openBrowser(ctx, address) },
	}, os.Args[1:])
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// openBrowser asks the system to open an address in the person's browser. The address is a
// single argument, never a shell command line.
func openBrowser(ctx context.Context, address string) error {
	if !strings.HasPrefix(address, "https://") && !strings.HasPrefix(address, "http://") {
		return fmt.Errorf("not a web address")
	}

	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.CommandContext(ctx, name, address)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("open the browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	return nil
}
