package updater

import (
	"archive/zip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/procly/procly/internal/release"
	"golang.org/x/mod/semver"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const maxArchive = 128 * 1024 * 1024

var ErrNoRelease = errors.New("no stable release has been published yet")
var errNotFound = errors.New("official repository or release was not found")

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}
type Release struct {
	Tag        string  `json:"tag_name"`
	Notes      string  `json:"body"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}
type Client struct {
	HTTP   *http.Client
	Source release.Source
}

func New() (*Client, error) {
	s, e := release.Official()
	if e != nil {
		return nil, e
	}
	return &Client{Source: s, HTTP: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("insecure update redirect")
		}
		switch req.URL.Hostname() {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("untrusted update redirect")
	}}}, nil
}
func Version(v string) string {
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}
func Newer(current, available string) (bool, error) {
	available = Version(available)
	if !semver.IsValid(available) || semver.Prerelease(available) != "" {
		return false, errors.New("release must have a stable semantic version")
	}
	current = Version(current)
	if !semver.IsValid(current) {
		return true, nil
	}
	return semver.Compare(available, current) > 0, nil
}
func (c *Client) get(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", address, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("User-Agent", "procly-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	response, e := c.HTTP.Do(req)
	if e != nil {
		return nil, fmt.Errorf("contact release service: %w", e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		switch response.StatusCode {
		case 404:
			return nil, errNotFound
		case 403, 429:
			return nil, errors.New("GitHub refused the request or its API rate limit was reached")
		}
		return nil, fmt.Errorf("release service returned HTTP %d", response.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("release response exceeds size limit")
	}
	return b, nil
}
func (c *Client) Latest(ctx context.Context) (Release, error) {
	var r Release
	b, e := c.get(ctx, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", c.Source.Owner, c.Source.Repository), 2*1024*1024)
	if errors.Is(e, errNotFound) {
		// Distinguish an empty release list from an inaccessible repository.
		if _, repoErr := c.get(ctx, fmt.Sprintf("https://api.github.com/repos/%s/%s", c.Source.Owner, c.Source.Repository), 2*1024*1024); repoErr != nil {
			return r, repoErr
		}
		return r, ErrNoRelease
	}
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.Draft || r.Prerelease {
		return r, errors.New("latest release is not stable")
	}
	if _, e = Newer("dev", r.Tag); e != nil {
		return r, e
	}
	return r, nil
}
func AssetName(version, goos, arch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("procly_%s_%s_%s%s", strings.TrimPrefix(version, "v"), goos, arch, ext)
}
func (c *Client) asset(r Release, name string) (Asset, error) {
	var found *Asset
	for _, a := range r.Assets {
		if a.Name != name {
			continue
		}
		if found != nil {
			return a, errors.New("duplicate release asset")
		}
		u, e := url.Parse(a.URL)
		if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || !strings.HasPrefix(u.Path, "/"+c.Source.Owner+"/"+c.Source.Repository+"/releases/download/") {
			return a, errors.New("release asset is not hosted by the official repository")
		}
		copy := a
		found = &copy
	}
	if found == nil {
		return Asset{}, fmt.Errorf("release has no asset %s", name)
	}
	return *found, nil
}
func VerifyManifest(publicKey string, manifest, signature []byte) error {
	key, e := base64.StdEncoding.DecodeString(publicKey)
	if e != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("invalid release signing public key")
	}
	sig, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(signature)))
	if e != nil || !ed25519.Verify(key, manifest, sig) {
		return errors.New("release signature verification failed")
	}
	return nil
}
func checksum(manifest []byte, name string) (string, error) {
	found := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if found != "" {
				return "", errors.New("duplicate checksum")
			}
			found = fields[0]
		}
	}
	raw, e := hex.DecodeString(found)
	if e != nil || len(raw) != sha256.Size {
		return "", errors.New("signed manifest has no valid checksum for this platform")
	}
	return strings.ToLower(found), nil
}

// Prepare downloads to a private temporary directory, authenticates the signed
// checksums, and extracts only the expected executable. Caller removes tempDir.
func (c *Client) Prepare(ctx context.Context, r Release, progress func(int64, int64)) (tempDir, executable string, err error) {
	name := AssetName(r.Tag, runtime.GOOS, runtime.GOARCH)
	archive, e := c.asset(r, name)
	if e != nil {
		return "", "", e
	}
	if archive.Size <= 0 || archive.Size > maxArchive {
		return "", "", errors.New("invalid release archive size")
	}
	manifestAsset, e := c.asset(r, "checksums.txt")
	if e != nil {
		return "", "", e
	}
	sigAsset, e := c.asset(r, "checksums.txt.sig")
	if e != nil {
		return "", "", e
	}
	manifest, e := c.get(ctx, manifestAsset.URL, 1024*1024)
	if e != nil {
		return "", "", e
	}
	signature, e := c.get(ctx, sigAsset.URL, 4096)
	if e != nil {
		return "", "", e
	}
	if e = VerifyManifest(c.Source.PublicKey, manifest, signature); e != nil {
		return "", "", e
	}
	want, e := checksum(manifest, name)
	if e != nil {
		return "", "", e
	}
	dir, e := os.MkdirTemp("", "procly-update-")
	if e != nil {
		return "", "", e
	}
	defer func() {
		if err != nil {
			os.RemoveAll(dir)
		}
	}()
	req, e := http.NewRequestWithContext(ctx, "GET", archive.URL, nil)
	if e != nil {
		return "", "", e
	}
	response, e := c.HTTP.Do(req)
	if e != nil {
		return "", "", e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", "", fmt.Errorf("archive download returned HTTP %d", response.StatusCode)
	}
	path := filepath.Join(dir, name)
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", "", e
	}
	hash := sha256.New()
	var written int64
	buffer := make([]byte, 64*1024)
	reader := io.LimitReader(response.Body, maxArchive+1)
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, e = f.Write(buffer[:n]); e != nil {
				f.Close()
				return "", "", e
			}
			hash.Write(buffer[:n])
			written += int64(n)
			if progress != nil {
				progress(written, archive.Size)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			f.Close()
			return "", "", readErr
		}
	}
	closeErr := f.Close()
	if closeErr != nil {
		return "", "", closeErr
	}
	if written != archive.Size || hex.EncodeToString(hash.Sum(nil)) != want {
		return "", "", errors.New("download checksum or size mismatch")
	}
	executable = filepath.Join(dir, "procly.exe")
	if e = Extract(path, executable); e != nil {
		return "", "", e
	}
	return dir, executable, nil
}
func Extract(archive, target string) error {
	z, e := zip.OpenReader(archive)
	if e != nil {
		return e
	}
	defer z.Close()
	var binary *zip.File
	for _, f := range z.File {
		if f.Name == "procly.exe" {
			if binary != nil {
				return errors.New("duplicate executable in archive")
			}
			binary = f
			continue
		}
		// Documentation is permitted, but never extracted.
		if f.Name != "README.md" && f.Name != "LICENSE" {
			return fmt.Errorf("unexpected archive entry %q", f.Name)
		}
	}
	if binary == nil || binary.Mode()&os.ModeSymlink != 0 || binary.UncompressedSize64 > maxArchive {
		return errors.New("invalid executable entry")
	}
	src, e := binary.Open()
	if e != nil {
		return e
	}
	defer src.Close()
	dst, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return e
	}
	_, e = io.Copy(dst, io.LimitReader(src, maxArchive+1))
	return errors.Join(e, dst.Close())
}
