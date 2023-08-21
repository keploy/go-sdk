package mock

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keploy/go-sdk/pkg/keploy"
	"go.uber.org/zap"

	"fmt"
	"os"
)

var (
	logger *zap.Logger
)

type Config struct {
	Mode      keploy.Mode // Keploy mode on which unit test will run. Possible values: MODE_TEST or MODE_RECORD. Default: MODE_TEST
	TestSuite string      // TestSuite name to record the mock or test the mocks
	Path      string      // Path in which Keploy "/mocks" will be generated. Default: current working directroy.
}

func NewContext(conf Config) {
	var (
		mode      = keploy.MODE_TEST
		err       error
		path      string = conf.Path
		keployCmd string
	)

	// use current directory, if path is not provided or relative in config
	if conf.Path == "" {
		path, err = os.Getwd()
		if err != nil {
			logger.Error("Failed to get the path of current directory", zap.Error(err))
		}
	} else if conf.Path[0] != '/' {
		path, err = filepath.Abs(conf.Path)
		if err != nil {
			logger.Error("Failed to get the absolute path from relative conf.path", zap.Error(err))
		}
	}

	if conf.TestSuite == "" {
		logger.Error("Failed to get test suite name")
	}

	appPid := os.Getpid()

	recordCmd := "sudo -E /usr/local/bin/keploy mockRecord --pid " + strconv.Itoa(appPid) + " --path " + path + " --delay 5" + " --testSuite " + conf.TestSuite
	testCmd := "sudo -E /usr/local/bin/keploy mockTest --pid " + strconv.Itoa(appPid) + " --path " + path + " --delay 5" + " --testSuite " + conf.TestSuite

	if keploy.Mode(conf.Mode).Valid() {
		mode = keploy.Mode(conf.Mode)
	} else {
		logger.Error("Failed to get mode")
	}

	if mode == keploy.MODE_TEST {
		keployCmd = testCmd
	} else {
		keployCmd = recordCmd
	}

	parts := strings.Fields(keployCmd)
	cmd := exec.Command(parts[0], parts[1:]...)
	// cmd.Stdout = os.Stdout
	// cmd.Stderr = os.Stderr
	go func() {
		err := cmd.Run()
		if err != nil {
			logger.Error("Failed to run command: %v\n", zap.Error(err))
			return
		}
	}()
	time.Sleep(20 * time.Second)
}

func KillProcessOnPort() {
	port := 16789
	cmd := exec.Command("sudo", "lsof", "-t", "-i:"+strconv.Itoa(port))
	output, err := cmd.Output()
	if err != nil {
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
