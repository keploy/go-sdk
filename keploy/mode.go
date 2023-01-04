package keploy

import (
	"github.com/keploy/go-sdk/internal/keploy"
)

const (
	MODE_RECORD keploy.Mode = keploy.MODE_RECORD
	MODE_TEST   keploy.Mode = keploy.MODE_TEST
	MODE_OFF    keploy.Mode = keploy.MODE_OFF
)

// GetMode returns the mode of the keploy SDK
func GetMode() keploy.Mode {
	return keploy.GetMode()
}

// SetTestMode sets the keploy SDK mode to MODE_TEST
func SetTestMode() {
	_ = keploy.SetMode(MODE_TEST)
}
