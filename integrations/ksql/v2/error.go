package v2

import (
	"database/sql/driver"
	"io"
)

func ConvertKError(err error) error {
	if err == nil {
		return nil
	}
	// return the sql/driver error which is matching the parameter error string
	str := err.Error()
	switch str {
	case driver.ErrBadConn.Error():
		return driver.ErrBadConn
	case driver.ErrRemoveArgument.Error():
		return driver.ErrRemoveArgument
	case driver.ErrSkip.Error():
		return driver.ErrSkip
	case io.EOF.Error():
		return io.EOF
	default:
		return err
	}
}
