package httprc

import "errors"

var errResourceAlreadyExists = errors.New(`resource already exists`)

func ErrResourceAlreadyExists() error {
	return errResourceAlreadyExists
}

var errAlreadyRunning = errors.New(`client is already running`)

func ErrAlreadyRunning() error {
	return errAlreadyRunning
}
