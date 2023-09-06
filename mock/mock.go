package mock

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keploy/go-sdk/v2/pkg/keploy"
	"go.uber.org/zap"

	"fmt"
	"os"
)

var (
	logger *zap.Logger
)

type Config struct {
	Mode             keploy.Mode // Keploy mode on which unit test will run. Possible values: MODE_TEST or MODE_RECORD. Default: MODE_TEST
	TestSuite        string      // TestSuite name to record the mock or test the mocks
	Path             string      // Path in which Keploy "/mocks" will be generated. Default: current working directroy.
	EnableKeployLogs bool
}

func NewContext(conf Config) {
	var (
		mode      = keploy.MODE_TEST
		err       error
		path      string = conf.Path
		keployCmd string
	)

	logger, _ = zap.NewDevelopment()
	defer func() {
		_ = logger.Sync()
	}()

	KillProcessOnPort()

	if keploy.Mode(conf.Mode).Valid() {
		mode = keploy.Mode(conf.Mode)
	} else {
		logger.Error("Failed to get mode, running Tests with Keploy MODE_OFF")
		mode = keploy.MODE_OFF
	}

	// use current directory, if path is not provided or relative in config
	if conf.Path == "" {
		path, err = os.Getwd()
		if err != nil {
			logger.Error("Failed to get the path of current directory, running Tests with Keploy OFF_MODE", zap.Error(err))
			mode = keploy.MODE_OFF
		}
	} else if conf.Path[0] != '/' {
		path, err = filepath.Abs(conf.Path)
		if err != nil {
			logger.Error("Failed to get the absolute path from relative conf.path, running Tests with Keploy MODE_OFF", zap.Error(err))
			mode = keploy.MODE_OFF
		}
	}

	if conf.TestSuite == "" {
		logger.Error("Failed to get test suite name, running Tests with Keploy MODE_OFF")
		mode = keploy.MODE_OFF
	}

	if mode == keploy.MODE_RECORD {
		if _, err := os.Stat(path + "/keploy/" + conf.TestSuite); !os.IsNotExist(err) {
			cmd := exec.Command("sudo", "rm", "-rf", path+"/keploy/"+conf.TestSuite)
			cmdOutput, err := cmd.CombinedOutput()
			if err != nil {
				logger.Error("Failed to delete existing directory, running Tests with Keploy MODE_OFF", zap.Error(err), zap.String("cmdOutput", string(cmdOutput)))
				return
			}
		}
	}

	if mode == keploy.MODE_OFF {
		return
	}

	appPid := os.Getpid()

	recordCmd := "sudo -E /usr/local/bin/keploy mockRecord --pid " + strconv.Itoa(appPid) + " --path " + path + " --delay 5" + " --testSuite " + conf.TestSuite
	testCmd := "sudo -E /usr/local/bin/keploy mockTest --pid " + strconv.Itoa(appPid) + " --path " + path + " --delay 5" + " --testSuite " + conf.TestSuite

	if mode == keploy.MODE_TEST {
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
	if _, ok := err.(*exec.ExitError); ok && len(output) == 0 {
		logger.Debug("No process found for port", zap.Int("port", port))
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
