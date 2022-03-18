package ksql

import "database/sql/driver"

func convertKError(err error) error {
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
	default:
		return err
	}
}
