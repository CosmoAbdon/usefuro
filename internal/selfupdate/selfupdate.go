// Package selfupdate replaces the running binary with the latest GitHub
// release (furo update / furo-server update).
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const repo = "CosmoAbdon/usefuro"

var client = &http.Client{Timeout: 5 * time.Minute}

func get(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "furo-selfupdate")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// LatestTag returns the tag of the latest GitHub release (e.g. "v0.1.2").
func LatestTag() (string, error) {
	resp, err := get("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.TagName == "" {
		return "", fmt.Errorf("no releases found for %s", repo)
	}
	return out.TagName, nil
}

// Run updates the current executable (named binary, e.g. "furo") to the
// latest release. No-op when already on it.
func Run(binary, currentVersion string) error {
	tag, err := LatestTag()
	if err != nil {
		return fmt.Errorf("check latest release: %w", err)
	}
	version := strings.TrimPrefix(tag, "v")
	if version == currentVersion {
		fmt.Printf("%s %s is already the latest release\n", binary, currentVersion)
		return nil
	}
	if currentVersion == "dev" || strings.HasSuffix(currentVersion, "-dev") {
		fmt.Printf("current build is %s (from source); installing release %s over it\n", currentVersion, tag)
	}

	ext := ".tar.gz"
	if runtime.GOOS == "windows" {
		ext = ".zip"
	}
	asset := fmt.Sprintf("%s_%s_%s_%s%s", binary, version, runtime.GOOS, runtime.GOARCH, ext)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s/", repo, tag)

	fmt.Printf("downloading %s ...\n", asset)
	archive, err := download(base + asset)
	if err != nil {
		return err
	}
	if err := verifyChecksum(base+"checksums.txt", asset, archive); err != nil {
		return err
	}
	binName := binary
	if runtime.GOOS == "windows" {
		binName += ".exe"
	}
	bin, err := extractBinary(archive, binName)
	if err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if err := replaceExecutable(exe, bin); err != nil {
		return err
	}
	fmt.Printf("updated %s: %s → %s (%s)\n", binary, currentVersion, version, exe)
	return nil
}

// replaceExecutable swaps the file at exe for the new bytes. On Windows a
// running executable cannot be overwritten but CAN be renamed, so the old
// one is moved aside first (the leftover .old is cleaned on the next run).
func replaceExecutable(exe string, bin []byte) error {
	old := exe + ".old"
	os.Remove(old) // leftover from a previous windows update, if any
	tmp := exe + ".new"
	if err := os.WriteFile(tmp, bin, 0o755); err != nil {
		return fmt.Errorf("write %s (need write access to %s — try sudo): %w", tmp, filepath.Dir(exe), err)
	}
	if runtime.GOOS == "windows" {
		if err := os.Rename(exe, old); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("move current executable aside: %w", err)
		}
	}
	if err := os.Rename(tmp, exe); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	if runtime.GOOS == "windows" {
		os.Remove(old) // fails while this process runs — next update cleans it
	}
	return nil
}

func download(url string) ([]byte, error) {
	resp, err := get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

func verifyChecksum(checksumsURL, asset string, archive []byte) error {
	resp, err := get(checksumsURL)
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	defer resp.Body.Close()
	sums, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("%s not found in checksums.txt", asset)
	}
	got := sha256.Sum256(archive)
	if hex.EncodeToString(got[:]) != want {
		return fmt.Errorf("checksum mismatch for %s", asset)
	}
	return nil
}

// extractBinary pulls the named file out of a .tar.gz or .zip archive.
func extractBinary(archive []byte, name string) ([]byte, error) {
	if len(archive) >= 4 && string(archive[:4]) == "PK\x03\x04" {
		return extractFromZip(archive, name)
	}
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return nil, err
		}
		if filepath.Base(hdr.Name) == name && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(io.LimitReader(tr, 500<<20))
		}
	}
}

func extractFromZip(archive []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, 500<<20))
		}
	}
	return nil, fmt.Errorf("%s not found in archive", name)
}
