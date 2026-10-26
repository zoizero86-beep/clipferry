package sysclip

import (
	"syscall"
	"unsafe"

	"github.com/wailsapp/wails/v3/pkg/w32"
	"golang.org/x/sys/windows"
)

var (
	user32                      = windows.NewLazySystemDLL("user32.dll")
	procSequenceNumber          = user32.NewProc("GetClipboardSequenceNumber")
	procRegisterClipboardFormat = user32.NewProc("RegisterClipboardFormatW")
	procIsFormatAvailable       = user32.NewProc("IsClipboardFormatAvailable")

	// Formats password managers set to keep secrets out of clipboard
	// history and monitors (documented by Microsoft for Windows clipboard history).
	concealFormats = []uintptr{
		registerFormat("ExcludeClipboardContentFromMonitorProcessing"),
		registerFormat("Clipboard Viewer Ignore"),
	}
)

func registerFormat(name string) uintptr {
	p, _ := syscall.UTF16PtrFromString(name)
	r, _, _ := procRegisterClipboardFormat.Call(uintptr(unsafe.Pointer(p)))
	return r
}

func changeCount() int64 {
	r, _, _ := procSequenceNumber.Call()
	return int64(r)
}

func text() (string, error) {
	s, err := w32.GetClipboardText()
	if err != nil {
		// No text on the clipboard (an image, files, …) is not an error for us.
		return "", nil
	}
	return s, nil
}

func setText(s string) error { return w32.SetClipboardText(s) }

func available(format uintptr) bool {
	if format == 0 {
		return false
	}
	r, _, _ := procIsFormatAvailable.Call(format)
	return r != 0
}

func concealed() bool {
	for _, f := range concealFormats {
		if available(f) {
			return true
		}
	}
	return false
}
