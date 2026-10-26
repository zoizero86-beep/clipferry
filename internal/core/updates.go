package core

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/bkrdmrcioglu/clipferry/internal/locale"
	"github.com/bkrdmrcioglu/clipferry/internal/update"
)

// UpdateState is what the UI shows about updates.
type UpdateState struct {
	Current   string     `json:"current"`
	Latest    string     `json:"latest,omitempty"`
	Available bool       `json:"available"`
	Status    string     `json:"status"` // idle | checking | downloading | installing | error | unsupported
	Error     string     `json:"error,omitempty"`
	Notes     string     `json:"notes,omitempty"`
	PageURL   string     `json:"page_url,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type updater struct {
	client *update.Client
	quit   func()

	mu    sync.Mutex
	state UpdateState
	rel   *update.Release
}

// SetUpdater enables updates from the release feed. quit is called to exit
// the app once the new version is ready to take over.
func (e *Engine) SetUpdater(c *update.Client, quit func()) {
	u := &updater{client: c, quit: quit, state: UpdateState{Current: Version, Status: "idle"}}
	if !update.IsRelease(Version) {
		u.state.Status = "unsupported" // development build
	}
	e.mu.Lock()
	e.updater = u
	e.mu.Unlock()
}

// Update returns the update state, or nil when updates are not available.
func (e *Engine) Update() *UpdateState {
	e.mu.Lock()
	u := e.updater
	e.mu.Unlock()
	if u == nil {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.state
	return &s
}

func (e *Engine) setUpdate(u *updater, f func(*UpdateState)) {
	u.mu.Lock()
	f(&u.state)
	s := u.state
	u.mu.Unlock()
	e.emit("update", s)
}

// CheckUpdate asks the release feed for a newer version.
func (e *Engine) CheckUpdate(ctx context.Context) (UpdateState, error) {
	e.mu.Lock()
	u := e.updater
	e.mu.Unlock()
	if u == nil {
		return UpdateState{}, errors.New("updates are not available here")
	}
	if !update.IsRelease(Version) {
		return *e.Update(), errors.New("this is a development build; updates are off")
	}
	e.setUpdate(u, func(s *UpdateState) { s.Status, s.Error = "checking", "" })
	r, err := u.client.Latest(ctx)
	now := time.Now()
	if err != nil {
		e.setUpdate(u, func(s *UpdateState) { s.Status, s.Error, s.CheckedAt = "error", err.Error(), &now })
		return *e.Update(), err
	}
	u.mu.Lock()
	u.rel = r
	u.mu.Unlock()
	e.setUpdate(u, func(s *UpdateState) {
		s.Status, s.CheckedAt, s.Latest = "idle", &now, r.Version
		s.Available = update.Newer(r.Version, Version) && r.Installable()
		s.Notes, s.PageURL = r.Notes, r.PageURL
	})
	return *e.Update(), nil
}

// InstallUpdate downloads, verifies and installs the newest version, then
// restarts the app.
func (e *Engine) InstallUpdate(ctx context.Context) error {
	e.mu.Lock()
	u := e.updater
	e.mu.Unlock()
	if u == nil {
		return errors.New("updates are not available here")
	}
	u.mu.Lock()
	r, busy := u.rel, u.state.Status == "downloading" || u.state.Status == "installing"
	available := u.state.Available
	u.mu.Unlock()
	if busy {
		return nil
	}
	if r == nil || !available {
		return errors.New("no update is available")
	}
	fail := func(err error) error {
		e.setUpdate(u, func(s *UpdateState) { s.Status, s.Error = "error", err.Error() })
		return err
	}
	e.setUpdate(u, func(s *UpdateState) { s.Status, s.Error = "downloading", "" })
	dir, err := os.MkdirTemp("", "clipferry-update-")
	if err != nil {
		return fail(err)
	}
	path, err := u.client.Download(ctx, r, dir)
	if err != nil {
		os.RemoveAll(dir)
		return fail(err)
	}
	e.setUpdate(u, func(s *UpdateState) { s.Status = "installing" })
	e.opts.Logf("installing update %s → %s", Version, r.Version)
	if err := update.Apply(path, u.quit); err != nil {
		os.RemoveAll(dir)
		return fail(err)
	}
	return nil
}

// updateLoop checks for updates shortly after start and then every few
// hours, installing them right away when automatic updates are on.
func (e *Engine) updateLoop(ctx context.Context) {
	if e.Update() == nil || !update.IsRelease(Version) {
		return
	}
	delay := 20 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = 6 * time.Hour
		st, err := e.CheckUpdate(ctx)
		if err != nil {
			e.opts.Logf("update check: %v", err)
			delay = time.Hour
			continue
		}
		if st.Available && e.Settings().AutoUpdate {
			if err := e.InstallUpdate(ctx); err != nil {
				e.opts.Logf("update install: %v", err)
			}
		}
	}
}

// Supported UI languages.
var languages = map[string]bool{"en": true, "tr": true}

// Language is the UI language in effect: the user's choice, or the system's
// when that is supported, or English.
func (e *Engine) Language() string {
	if l := e.Settings().Language; languages[l] {
		return l
	}
	if l := locale.Base(locale.Preferred()); languages[l] {
		return l
	}
	return "en"
}
