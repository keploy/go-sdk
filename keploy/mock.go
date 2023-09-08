package keploy

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"fmt"
	"os"
)

var (
	logger *zap.Logger
)

type Config struct {
	Mode             Mode   // Keploy mode on which unit test will run. Possible values: MODE_TEST or MODE_RECORD. Default: MODE_TEST
	Name             string // Name to record the mock or test the mocks
	Path             string // Path in which Keploy "/mocks" will be generated. Default: current working directroy.
	EnableKeployLogs bool
	Delay            int
}

func New(conf Config) error {

	var (
		mode      = MODE_OFF
		err       error
		path      string = conf.Path
		keployCmd string
		delay     int = 5
	)

	logger, _ = zap.NewDevelopment()
	defer func() {
		_ = logger.Sync()
	}()

	// killing keploy instance if it is running already
	KillProcessOnPort()

	if Mode(conf.Mode).Valid() {
		mode = Mode(conf.Mode)
	} else {
		return errors.New("provided keploy mode is invalid, either use MODE_RECORD/MODE_TEST/MODE_OFF")
	}

	if conf.Delay > 5 {
		delay = conf.Delay
	}

	if mode == MODE_OFF {
		return nil
	}

	// use current directory, if path is not provided or relative in config
	if path == "" {
		path, err = os.Getwd()
		if err != nil {
			return fmt.Errorf("no specific path provided and failed to get current working directory %w", err)
		}
		logger.Info("no specific path provided; defaulting to the current working directory", zap.String("currentDirectoryPath", path))
	} else if path[0] != '/' {
		path, err = filepath.Abs(path)
		if err != nil {
			return fmt.Errorf("failed to get the absolute path from provided path %w", err)
		}
	} else {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return fmt.Errorf("provided path does not exist %w", err)
		}
		logger.Info("using provided path to store mocks", zap.String("providedPath", path))
	}

	if conf.Name == "" {
		return errors.New("provided mock name is empty")
	}

	if mode == MODE_RECORD {
		if _, err := os.Stat(path + "/keploy/" + conf.Name); !os.IsNotExist(err) {
			cmd := exec.Command("sudo", "rm", "-rf", path+"/keploy/"+conf.Name)
			_, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("failed to replace existing mock file %w", err)
			}
		}
	}

	appPid := os.Getpid()

	recordCmd := "sudo -E /usr/local/bin/keploy mockRecord --pid " + strconv.Itoa(appPid) + " --path " + path + " --mockName " + conf.Name + " --debug"
	testCmd := "sudo -E /usr/local/bin/keploy mockTest --pid " + strconv.Itoa(appPid) + " --path " + path + " --mockName " + conf.Name + " --debug"

	if mode == MODE_TEST {
		keployCmd = testCmd
	} else {
		keployCmd = recordCmd
	}

	parts := strings.Fields(keployCmd)
	cmd := exec.Command(parts[0], parts[1:]...)
	if conf.EnableKeployLogs {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}

	if _, err := exec.LookPath("keploy"); err != nil {
		return fmt.Errorf("keploy binary not found, please ensure it is installed. Host OS: %s, Architecture: %s. For installing please follow instructions https://github.com/keploy/keploy#quick-installation", runtime.GOOS, runtime.GOARCH)
	}

	errChan := make(chan error)

	go func() {
		err := cmd.Run()
		if err != nil {
			errChan <- err
		}
		close(errChan)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			return err
		}
		return nil
	case <-time.After(time.Duration(delay) * time.Second):
		return nil
	}
}

func KillProcessOnPort() {
	port := 16789
	cmd := exec.Command("sudo", "lsof", "-t", "-i:"+strconv.Itoa(port))
	output, err := cmd.Output()
	if _, ok := err.(*exec.ExitError); ok && len(output) == 0 {
		return
	} else if err != nil {
		logger.Error("Failed to execute lsof: %v\n", zap.Error(err))
		return
	}
	appPid := os.Getpid()
	pids := strings.Split(strings.Trim(string(output), "\n"), "\n")
	for _, pid := range pids {
		if pid != strconv.Itoa(appPid) {
			forceKillProcessByPID(pid)
		}
	}
}

func forceKillProcessByPID(pid string) {
	cmd := exec.Command("sudo", "kill", "-9", pid)
	if err := cmd.Run(); err != nil {
		logger.Error(fmt.Sprintf("Failed to kill process with PID %s:", pid), zap.Error(err))
	}
}
