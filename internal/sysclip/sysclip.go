// Package sysclip reads and writes the operating system clipboard for the
// desktop app. Besides plain text it reports a change counter, so polling
// is nearly free while nothing is copied, and whether the current content was
// marked as concealed by a password manager.
package sysclip

import "errors"

// ErrUnsupported is returned on platforms without an implementation.
var ErrUnsupported = errors.New("sysclip: clipboard access is not supported on this platform")

// Clipboard is the system clipboard.
type Clipboard struct{}

// New returns the system clipboard.
func New() *Clipboard { return &Clipboard{} }

// Text returns the clipboard's text, or "" when it holds no text.
func (Clipboard) Text() (string, error) { return text() }

// SetText replaces the clipboard's content with text.
func (Clipboard) SetText(s string) error { return setText(s) }

// ChangeCount increases whenever any application changes the clipboard.
func (Clipboard) ChangeCount() int64 { return changeCount() }

// Concealed reports whether the current content was marked by its source as
// a password or other secret that clipboard managers should not keep — the
// convention used by 1Password, Bitwarden, KeePass and others.
func (Clipboard) Concealed() bool { return concealed() }
