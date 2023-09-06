package keploy

// Mode represents the mode at which the SDK is operating
// MODE_RECORD is for recording API calls to generate testcases
// MODE_TEST is for testing the application on previous recorded testcases
// MODE_OFF disables keploy SDK automatically from the application
type Mode string

const (
	MODE_RECORD Mode = "record"
	MODE_TEST   Mode = "test"
	MODE_OFF    Mode = "off"
)

// Valid checks if the provided mode is valid
func (m Mode) Valid() bool {
	if m == MODE_RECORD || m == MODE_TEST || m == MODE_OFF {
		return true
	}
	return false
}
