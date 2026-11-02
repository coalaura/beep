//go:build !linux && !windows

package main

import "errors"

func beep() error {
	return errors.New("not supported")
}
