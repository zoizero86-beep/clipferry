package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestNewer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.2.0", "0.1.0", true}, {"0.1.10", "0.1.9", true}, {"1.0.0", "0.9.9", true},
		{"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false},
	}
	for _, c := range cases {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%s, %s) = %v", c.a, c.b, got)
		}
	}
	if IsRelease("0.1.0-3-gabc123") || !IsRelease("0.2.0") {
		t.Error("IsRelease misclassifies versions")
	}
}

func feed(t *testing.T, payload []byte, sums string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":"v0.2.0","body":"notes","html_url":"https://example.com/r","assets":[
			{"name":"Clipferry-0.2.0-macos.dmg","browser_download_url":"%[1]s/dmg","size":%[2]d},
			{"name":"Clipferry-0.2.0-windows-setup.exe","browser_download_url":"%[1]s/exe","size":%[2]d},
			{"name":"SHA256SUMS","browser_download_url":"%[1]s/sums","size":1}]}`, srv.URL, len(payload))
	})
	mux.HandleFunc("/dmg", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux.HandleFunc("/exe", func(w http.ResponseWriter, r *http.Request) { w.Write(payload) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, sums) })
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDownloadVerifiesChecksum(t *testing.T) {
	payload := []byte("pretend this is an installer")
	sum := sha256.Sum256(payload)
	good := hex.EncodeToString(sum[:]) + "  Clipferry-0.2.0-macos.dmg\n" + hex.EncodeToString(sum[:]) + " *Clipferry-0.2.0-windows-setup.exe\n"

	for _, goos := range []string{"darwin", "windows"} {
		srv := feed(t, payload, good)
		c := &Client{API: srv.URL + "/latest", GOOS: goos}
		r, err := c.Latest(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if r.Version != "0.2.0" || !r.Installable() || r.Asset.Name != AssetName(goos, "0.2.0") {
			t.Fatalf("%s: release = %+v", goos, r)
		}
		path, err := c.Download(context.Background(), r, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		if b, _ := os.ReadFile(path); string(b) != string(payload) {
			t.Fatalf("%s: wrong file content", goos)
		}
	}

	// A tampered file is rejected and removed.
	bad := feed(t, []byte("tampered"), good)
	c := &Client{API: bad.URL + "/latest", GOOS: "darwin"}
	r, _ := c.Latest(context.Background())
	dir := t.TempDir()
	if _, err := c.Download(context.Background(), r, dir); !errors.Is(err, ErrChecksum) {
		t.Fatalf("tampered download: err = %v, want ErrChecksum", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatal("tampered file was left on disk")
	}

	// No published checksum, no install.
	none := feed(t, payload, "")
	c = &Client{API: none.URL + "/latest", GOOS: "darwin"}
	r, _ = c.Latest(context.Background())
	if _, err := c.Download(context.Background(), r, t.TempDir()); err == nil {
		t.Fatal("download without a checksum succeeded")
	}
}
