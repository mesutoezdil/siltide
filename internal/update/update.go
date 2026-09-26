// Package update moves a loose siltide binary to a newer release. Everything
// installed through a package manager updates itself; a binary someone
// downloaded with curl has no way to, and nothing tells it there is one.
//
// Two rules the code is built around, both about not leaving someone worse
// off than before they ran it:
//
//   - Nothing is written until the download's SHA-256 matches the checksum
//     published with the release. A substituted or truncated archive never
//     reaches the path the user runs.
//   - The last step is a rename, never a copy into place, so an interrupted
//     update cannot leave half a binary on PATH.
//
// Every decision here is a function over strings; only Fetch touches the
// network, and it is injected, so the whole path is tested without a request.
package update

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Fetch reads a URL. The caller supplies one so tests need no server.
type Fetch func(url string) ([]byte, error)

// ErrNoRelease is what a missing release reads as: nothing is published yet,
// or the channel has no build on it. It is a normal answer, not a fault.
var ErrNoRelease = errors.New("no release published yet")

// StatusError is an HTTP response that was not 200, so callers can tell a
// missing release from a network failure.
type StatusError struct {
	URL, Status string
	Code        int
}

func (e *StatusError) Error() string { return e.URL + ": " + e.Status }

// Release is the part of a GitHub release this needs.
type Release struct {
	Tag        string `json:"tag_name"`
	PreRelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// Version is the release a tag names, without the leading v.
func (r Release) Version() string { return strings.TrimPrefix(r.Tag, "v") }

const (
	latestURL   = "https://api.github.com/repos/moezdil/siltide/releases/latest"
	releasesURL = "https://api.github.com/repos/moezdil/siltide/releases?per_page=20"
	downloadURL = "https://github.com/moezdil/siltide/releases/download"
)

// Channel is which releases to consider.
type Channel int

const (
	// Stable is released versions.
	Stable Channel = iota
	// Dev is the rolling pre-releases cut from main.
	Dev
)

// ChannelOf is the channel a binary reporting this version came from, so
// `update` keeps someone where they are: a pre-release build offered only
// stable would be stranded, since the stable release it sits above is not an
// update and there would be nothing to install and no way to say so.
func ChannelOf(version string) Channel {
	if strings.Contains(strings.TrimPrefix(version, "v"), "-") {
		return Dev
	}
	return Stable
}

// Asset is the release file built for this machine.
func Asset() (string, error) {
	switch runtime.GOOS {
	case "linux", "darwin":
	default:
		return "", fmt.Errorf("no build for %s: see the releases page", runtime.GOOS)
	}
	switch runtime.GOARCH {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("no build for %s: see the releases page", runtime.GOARCH)
	}
	return fmt.Sprintf("siltide-%s-%s", runtime.GOOS, runtime.GOARCH), nil
}

// Latest is the newest release on a channel.
func Latest(get Fetch, ch Channel) (Release, error) {
	if ch == Stable {
		b, err := get(latestURL)
		if err != nil {
			return Release{}, noRelease(err)
		}
		var r Release
		if err := json.Unmarshal(b, &r); err != nil {
			return Release{}, fmt.Errorf("reading the latest release: %w", err)
		}
		if r.Tag == "" {
			return Release{}, fmt.Errorf("%w: the releases API named no version", ErrNoRelease)
		}
		return r, nil
	}
	b, err := get(releasesURL)
	if err != nil {
		return Release{}, noRelease(err)
	}
	var all []Release
	if err := json.Unmarshal(b, &all); err != nil {
		return Release{}, fmt.Errorf("reading the release list: %w", err)
	}
	for _, r := range all { // newest first, which is how the API returns them
		if !r.Draft {
			return r, nil
		}
	}
	return Release{}, ErrNoRelease
}

// Checksum is the published SHA-256 of one asset, read from checksums.txt.
func Checksum(sums []byte, asset string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && f[1] == asset && len(f[0]) == 64 {
			return f[0], true
		}
	}
	return "", false
}

// Verify reports whether the bytes are what the release published.
func Verify(b []byte, want string) error {
	sum := sha256.Sum256(b)
	if got := hex.EncodeToString(sum[:]); got != want {
		return fmt.Errorf("checksum mismatch: got %s, the release publishes %s", got, want)
	}
	return nil
}

// Install writes the binary next to path and renames it into place, keeping
// the mode of what it replaces.
func Install(path string, b []byte) error {
	dir := filepath.Dir(path)
	mode := os.FileMode(0o755)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, ".siltide-update-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // a no-op once the rename succeeded
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// Plan is what an update would do, worked out before anything is downloaded.
type Plan struct {
	From, To string
	Channel  Channel
	Asset    string
	URL      string
	SumsURL  string
}

// Newer reports whether the plan actually moves forward.
func (p Plan) Newer() bool { return p.From != p.To }

// Prepare works out what to move to, without fetching the binary itself.
func Prepare(get Fetch, version string, ch Channel) (Plan, error) {
	asset, err := Asset()
	if err != nil {
		return Plan{}, err
	}
	r, err := Latest(get, ch)
	if err != nil {
		return Plan{}, err
	}
	base := downloadURL + "/" + r.Tag
	return Plan{
		From:    strings.TrimPrefix(version, "v"),
		To:      r.Version(),
		Channel: ch,
		Asset:   asset,
		URL:     base + "/" + asset,
		SumsURL: base + "/checksums.txt",
	}, nil
}

// Run performs an update and reports what it did. path is the binary to
// replace, which is normally the running one.
func Run(get Fetch, path, version string, ch Channel, out io.Writer) error {
	if by, ok := Managed(path); ok {
		return fmt.Errorf("%s was installed with %s: update it there", path, by)
	}
	plan, err := Prepare(get, version, ch)
	if err != nil {
		return err
	}
	if !plan.Newer() {
		_, _ = fmt.Fprintf(out, "siltide %s is the newest on the %s channel\n", plan.From, channelName(ch))
		return nil
	}
	_, _ = fmt.Fprintf(out, "siltide %s -> %s\n", plan.From, plan.To)

	bin, err := get(plan.URL)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", plan.Asset, err)
	}
	sums, err := get(plan.SumsURL)
	if err != nil {
		return fmt.Errorf("release %s publishes no checksums: %w", plan.To, err)
	}
	want, ok := Checksum(sums, plan.Asset)
	if !ok {
		return fmt.Errorf("release %s publishes no checksum for %s", plan.To, plan.Asset)
	}
	if err := Verify(bin, want); err != nil {
		return err
	}
	if err := Install(path, bin); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "installed %s\n", path)
	return nil
}

// noRelease reads a 404 as "there is nothing published", which is what it
// means on the releases endpoints, and leaves every other failure alone.
func noRelease(err error) error {
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		return ErrNoRelease
	}
	return err
}

func channelName(ch Channel) string {
	if ch == Dev {
		return "dev"
	}
	return "stable"
}

// maxDownload caps what a fetch will read into memory. The binary is a few
// tens of megabytes; this is a bound on a bad or hostile response, not a
// prediction about the release.
const maxDownload = 256 << 20

// HTTP is the Fetch used outside tests.
func HTTP() Fetch {
	client := &http.Client{Timeout: 5 * time.Minute}
	return func(url string) ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "siltide")
		req.Header.Set("Accept", "application/octet-stream, application/json")
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, &StatusError{URL: url, Status: resp.Status, Code: resp.StatusCode}
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxDownload {
			return nil, fmt.Errorf("%s: larger than %d bytes", url, maxDownload)
		}
		return b, nil
	}
}

// Self is the binary that is running, with symlinks resolved so an update
// replaces the real file rather than a link on PATH.
func Self() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// managed names what owns a binary at this path, for the installs that must
// not be updated in place. Replacing a packaged file leaves the package
// database describing a version that is no longer there, and the next upgrade
// of that package silently reverts the update.
//
// /usr/local/bin and ~/.local/bin are deliberately absent: that is where the
// install script puts a binary, and those are exactly the ones with no other
// way to update.
func Managed(path string) (string, bool) {
	// A segment anywhere in the path: these managers install under a store or
	// cellar and link the name onto PATH.
	for _, m := range []struct{ seg, by string }{
		{"/nix/store/", "nix"},
		{"/Cellar/", "brew"},
		{"/snap/", "snap"},
		{"/var/lib/flatpak/", "flatpak"},
	} {
		if strings.Contains(path, m.seg) {
			return m.by, true
		}
	}
	// A directory a system package owns outright.
	for _, dir := range []string{"/usr/bin", "/usr/sbin", "/bin", "/sbin", "/opt/homebrew/bin"} {
		if filepath.Dir(path) == dir {
			by := "your package manager"
			if dir == "/opt/homebrew/bin" {
				by = "brew"
			}
			return by, true
		}
	}
	return "", false
}
