package keploy

import (
	"context"
	"errors"
)

// Mode represents the mode at which the SDK is operating
// MODE_RECORD is for recording API calls to generate testcases
// MODE_TEST is for testing the application on previous recorded testcases
// MODE_OFF disables keploy SDK automatically from the application
type Mode string

type KctxType string

const (
	MODE_RECORD Mode     = "record"
	MODE_TEST   Mode     = "test"
	MODE_OFF    Mode     = "off"
	KCTX        KctxType = "KeployContext"
	KTime       KctxType = "KeployTime"
)

var (
	mode = MODE_OFF
)

// Valid checks if the provided mode is valid
func (m Mode) Valid() bool {
	if m == MODE_RECORD || m == MODE_TEST || m == MODE_OFF {
		return true
	}
	return false
}

// GetMode returns the mode of the keploy SDK
func GetMode() Mode {
	return mode
}

// SetTestMode sets the keploy SDK mode to MODE_TEST
func SetTestMode() {
	_ = SetMode(MODE_TEST)
}

// SetMode sets the keploy SDK mode
// error is returned if the mode is invalid
func SetMode(m Mode) error {
	if !m.Valid() {
		return errors.New("invalid mode: " + string(m))
	}
	mode = m
	return nil
}

// GetModeFromContext returns the mode on which SDK is configured by accessing environment variable.
func GetModeFromContext(ctx context.Context) Mode {
	kctx, err := GetState(ctx)
	if err != nil {
		return MODE_OFF
	}
	return kctx.Mode
}

// GetState returns value of "KeployContext" key-value pair which is stored in the request context.
func GetState(ctx context.Context) (*Context, error) {
	kctx := ctx.Value(KCTX)
	if kctx == nil {
		return nil, errors.New("failed to get Keploy context")
	}
	return kctx.(*Context), nil
}
