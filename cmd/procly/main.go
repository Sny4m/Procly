package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/cli"
	"github.com/procly/procly/internal/updater"
	"os"
	"os/signal"
	"syscall"
)

var version = "0.2.0-dev"

func main() { os.Exit(run()) }
func run() int {
	if len(os.Args) == 3 && os.Args[1] == "__update-helper" {
		if e := updater.RunHelper(os.Args[2]); e != nil {
			return 1
		}
		return 0
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Execute(ctx, version); err != nil {
		var exit cli.ExitError
		if errors.As(err, &exit) {
			return exit.Code
		}
		fmt.Fprintln(os.Stderr, "procly:", err)
		return 1
	}
	return 0
}
