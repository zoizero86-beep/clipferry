// Package update keeps the desktop app up to date from GitHub releases.
// A release is installed only if its file matches the SHA-256 published in
// the release's SHA256SUMS, which CI writes alongside the builds.
package update

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DefaultAPI is the GitHub endpoint describing the latest release.
const DefaultAPI = "https://api.github.com/repos/bkrdmrcioglu/clipferry/releases/latest"

// Release is a published version with the file for this platform.
type Release struct {
	Version   string `json:"version"`
	Notes     string `json:"notes"`
	PageURL   string `json:"page_url"`
	Asset     Asset  `json:"-"`
	Checksums Asset  `json:"-"`
}

// Asset is a downloadable release file.
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Client talks to the release feed.
type Client struct {
	API  string       // defaults to DefaultAPI
	HTTP *http.Client // defaults to a client with a timeout
	GOOS string       // defaults to runtime.GOOS
}

var releaseVersion = regexp.MustCompile(`^\d+\.\d+\.\d+$`)

// IsRelease reports whether v is a plain x.y.z version. Development builds
// (e.g. "0.1.0-3-gabc123") never update themselves.
func IsRelease(v string) bool { return releaseVersion.MatchString(v) }

// Newer reports whether version a is newer than b (both x.y.z).
func Newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		x, _ := strconv.Atoi(at(pa, i))
		y, _ := strconv.Atoi(at(pb, i))
		if x != y {
			return x > y
		}
	}
	return false
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return "0"
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// AssetName is the release file for a platform and version.
func AssetName(goos, version string) string {
	switch goos {
	case "darwin":
		return "Clipferry-" + version + "-macos.dmg"
	case "windows":
		return "Clipferry-" + version + "-windows-setup.exe"
	}
	return ""
}

// Latest fetches the newest release.
func (c *Client) Latest(ctx context.Context) (*Release, error) {
	api := c.API
	if api == "" {
		api = DefaultAPI
	}
	goos := c.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	req, err := http.NewRequestWithContext(ctx, "GET", api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := c.http().Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("checking for updates: %s", res.Status)
	}
	var gh struct {
		TagName string `json:"tag_name"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Draft   bool   `json:"draft"`
		Pre     bool   `json:"prerelease"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(res.Body).Decode(&gh); err != nil {
		return nil, fmt.Errorf("checking for updates: %w", err)
	}
	r := &Release{Version: strings.TrimPrefix(gh.TagName, "v"), Notes: gh.Body, PageURL: gh.HTMLURL}
	if !IsRelease(r.Version) || gh.Draft || gh.Pre {
		return nil, fmt.Errorf("latest release %q is not a stable version", gh.TagName)
	}
	want := AssetName(goos, r.Version)
	for _, a := range gh.Assets {
		switch a.Name {
		case want:
			r.Asset = Asset{a.Name, a.URL, a.Size}
		case "SHA256SUMS":
			r.Checksums = Asset{a.Name, a.URL, a.Size}
		}
	}
	return r, nil
}

// Installable reports whether r has everything needed to install it here.
func (r *Release) Installable() bool { return r.Asset.URL != "" && r.Checksums.URL != "" }

func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("download %s: %s", filepath.Base(url), res.Status)
	}
	return res, nil
}

// ErrChecksum means a downloaded file did not match the published checksum.
var ErrChecksum = errors.New("update: downloaded file does not match its published checksum")

// Download fetches r's file into dir and verifies it. It returns the path.
func (c *Client) Download(ctx context.Context, r *Release, dir string) (string, error) {
	if !r.Installable() {
		return "", errors.New("update: this release has no installer for this platform")
	}
	want, err := c.checksum(ctx, r)
	if err != nil {
		return "", err
	}
	res, err := c.get(ctx, r.Asset.URL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	path := filepath.Join(dir, r.Asset.Name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), res.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return "", err
	}
	if hex.EncodeToString(h.Sum(nil)) != want {
		os.Remove(path)
		return "", ErrChecksum
	}
	return path, nil
}

func (c *Client) checksum(ctx context.Context, r *Release) (string, error) {
	res, err := c.get(ctx, r.Checksums.URL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(io.LimitReader(res.Body, 1<<20))
	for sc.Scan() {
		// "<hex>  <name>" (sha256sum format; "*" marks binary mode)
		f := strings.Fields(sc.Text())
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == r.Asset.Name && len(f[0]) == 64 {
			return strings.ToLower(f[0]), nil
		}
	}
	return "", fmt.Errorf("update: no checksum published for %s", r.Asset.Name)
}

// Apply installs a downloaded, verified release and restarts the app. On
// success it calls quit; the replacement starts once this process exits.
func Apply(path string, quit func()) error { return apply(path, quit) }
