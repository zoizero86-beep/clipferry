//go:build !darwin && !windows

package update

import "errors"

func apply(string, func()) error {
	return errors.New("update: automatic updates are not supported on this platform")
}
