//go:build !darwin && !windows

package sysclip

func changeCount() int64    { return 0 }
func text() (string, error) { return "", ErrUnsupported }
func setText(string) error  { return ErrUnsupported }
func concealed() bool       { return false }
