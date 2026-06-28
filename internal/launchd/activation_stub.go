//go:build !darwin || !cgo

package launchd

import (
	"errors"
	"net"
)

func ActivatedListeners(name string) ([]net.Listener, error) {
	return nil, errors.New("launchd socket activation is only available on darwin with cgo")
}
