package keploy

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cov "github.com/keploy/go-sdk/v2/coverage"
	"go.uber.org/zap"
)

// Config holds all the options for initializing Keploy.
type Config struct {
	Mode           Mode   // MODE_RECORD, MODE_TEST or MODE_OFF
	Command        string // the shell command to start your application (e.g. "./myapp")
	Path           string // where to read/write keploy/* (defaults to cwd)
	Name           string // test‐set name: used as metadata (record) or -t (test)
	MuteKeployLogs bool   // suppress keploy CLI stdout/stderr
	Delay          int    // how long to wait (in seconds) before returning from New()
}

var (
	logger     *zap.Logger
	seenHashes = make(map[string]string)
	covClient  = cov.NewClient()
)

// New starts a keploy CLI in the background (record or test) and returns immediately.
func New(conf Config) error {
	var err error
	logger, _ = zap.NewDevelopment()
	defer logger.Sync()

	KillProcessOnPort()

	if !Mode(conf.Mode).Valid() {
		return errors.New("invalid mode: must be MODE_RECORD, MODE_TEST or MODE_OFF")
	}
	mode := Mode(conf.Mode)

	if mode == MODE_OFF {
		return nil
	}

	if strings.TrimSpace(conf.Command) == "" {
		return errors.New("Command is required (e.g. \"./myapp\")")
	}

	delay := 5
	if conf.Delay > 0 {
		delay = conf.Delay
	}

	path := conf.Path
	if path == "" {
		path, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get cwd: %w", err)
		}
	} else if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("invalid Path: %w", err)
		}
	}

	keployBin, err := exec.LookPath("keploy")
	if err != nil {
		return fmt.Errorf("keploy not found in $PATH: %w", err)
	}

	// in RECORD mode, do coverage‐hash dedup
	if mode == MODE_RECORD {
		if hash, _, err := covClient.DumpAndHash(); err == nil && hash != "" {
			if prev, dup := seenHashes[hash]; dup {
				logger.Info("duplicate interaction – skipping record", zap.String("previous", prev))
				covClient.ResetCoverage()
				return nil
			}
			seenHashes[hash] = conf.Name
			covClient.ResetCoverage()
		} else if err != nil {
			logger.Warn("coverage hash unavailable; proceeding without dedup", zap.Error(err))
		}
	}

	var args []string
	switch mode {
	case MODE_RECORD:
		args = []string{
			"record",
			"-c", conf.Command,
			"-p", path,
		}
	case MODE_TEST:
		args = []string{
			"test",
			"-c", conf.Command,
			"-p", path,
		}
	}

	if !conf.MuteKeployLogs {
		args = append(args, "--debug")
	}

	cmd := exec.Command(keployBin, args...)
	if !conf.MuteKeployLogs {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- cmd.Run()
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(time.Duration(delay) * time.Second):
		return nil
	}
}

// KillProcessOnPort will SIGTERM anything listening on your proxy‐port (default 16789).
func KillProcessOnPort() {
	port := 16789
	cmd := exec.Command("sudo", "lsof", "-t", "-i:"+strconv.Itoa(port))
	output, err := cmd.Output()
	if _, ok := err.(*exec.ExitError); ok && len(output) == 0 {
		return
	} else if err != nil {
		logger.Error("lsof failed", zap.Error(err))
		return
	}
	self := strconv.Itoa(os.Getpid())
	for _, pid := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if pid != self {
			exec.Command("sudo", "kill", "-TERM", pid).Run()
		}
	}
	time.Sleep(time.Second)
}
