package keploy

import (
	"os"
	"os/signal"
	"syscall"
)

// GracefulShutdown is used to signal the user application to exit when SIGTERM is triggered 
// from keploy test cmd. This function call can be used when the go application have not employed
// a graceful shutdown mechanism.
func GracefulShutdown() {
	stopper := make(chan os.Signal, 1)
	// listens for interrupt and SIGTERM signal
	signal.Notify(stopper, os.Interrupt, os.Kill, syscall.SIGKILL, syscall.SIGTERM)
	go func() {
		select {
		case <-stopper:
			os.Exit(0)
		}
	}()
}
