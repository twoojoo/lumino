package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
)

// Kind is how this copy of lumino was installed.
type Kind int

const (
	KindManual   Kind = iota // package manager / read-only location: link only
	KindAppImage             // $APPIMAGE
	KindTarball              // linux binary from the tar.gz
	KindWindows              // portable lumino.exe
	KindMacApp               // lumino.app bundle
)

// Plan says what an update would replace.
type Plan struct {
	Kind   Kind
	Target string // file or .app replaced in place
	Reason string // why the update is manual
}

// Detect inspects the running executable.
func Detect() Plan {
	exe, err := os.Executable()
	if err != nil {
		return Plan{Reason: err.Error()}
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	switch runtime.GOOS {
	case "linux":
		if ai := os.Getenv("APPIMAGE"); ai != "" {
			return writable(Plan{Kind: KindAppImage, Target: ai})
		}
		if strings.HasPrefix(exe, "/usr/") || strings.HasPrefix(exe, "/opt/") {
			return Plan{Reason: "installed by the system package manager: update it with apt/dnf or download the new package"}
		}
		return writable(Plan{Kind: KindTarball, Target: exe})
	case "windows":
		return writable(Plan{Kind: KindWindows, Target: exe})
	case "darwin":
		// …/lumino.app/Contents/MacOS/lumino
		app := filepath.Dir(filepath.Dir(filepath.Dir(exe)))
		if strings.HasSuffix(app, ".app") {
			return writable(Plan{Kind: KindMacApp, Target: app})
		}
		return writable(Plan{Kind: KindTarball, Target: exe})
	}
	return Plan{Reason: "self-update is not supported on " + runtime.GOOS}
}

// writable downgrades the plan to manual when the target can't be replaced.
func writable(p Plan) Plan {
	dir := filepath.Dir(p.Target)
	f, err := os.CreateTemp(dir, ".lumino-update-check-*")
	if err != nil {
		return Plan{Reason: dir + " is not writable: download the new version manually"}
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return p
}

// Install downloads, verifies and installs r according to p. progress
// receives downloaded/total bytes (total may be -1). On success the new
// version is in place and Restart starts it.
func Install(ctx context.Context, r *Release, p Plan, progress func(done, total int64)) error {
	if p.Kind == KindManual {
		return errors.New(p.Reason)
	}
	name := assetName(p.Kind, r.Version)
	url, ok := r.Assets[name]
	if !ok {
		return fmt.Errorf("release %s has no %s", r.Version, name)
	}
	sumsURL, ok1 := r.Assets[sumsName]
	sigURL, ok2 := r.Assets[sigName]
	if !ok1 || !ok2 {
		return errors.New("release is not signed (no " + sigName + "): download it manually")
	}
	sums, err := fetch(ctx, sumsURL, 1<<20)
	if err != nil {
		return err
	}
	sig, err := fetch(ctx, sigURL, 4<<10)
	if err != nil {
		return err
	}
	want, err := verifySums(sums, sig)
	if err != nil {
		return err
	}
	if want[name] == "" {
		return fmt.Errorf("%s is not listed in %s", name, sumsName)
	}

	dir := filepath.Dir(p.Target)
	tmp, err := os.CreateTemp(dir, ".lumino-update-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := download(ctx, url, tmp, want[name], progress); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	switch p.Kind {
	case KindAppImage:
		return replaceFile(tmp.Name(), p.Target)
	case KindTarball:
		bin := tmp.Name() + ".bin"
		defer os.Remove(bin)
		if err := extractTarBinary(tmp.Name(), "lumino", bin); err != nil {
			return err
		}
		return replaceFile(bin, p.Target)
	case KindWindows:
		bin := tmp.Name() + ".exe"
		defer os.Remove(bin)
		if err := extractZipFile(tmp.Name(), "lumino.exe", bin); err != nil {
			return err
		}
		return replaceRunningExe(bin, p.Target)
	case KindMacApp:
		return replaceApp(tmp.Name(), p.Target)
	}
	return errors.New("unsupported install kind")
}

func fetch(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lumino-updater")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("download %s: %s", url, resp.Status)
	}
	return resp, nil
}

type progressWriter struct {
	done, total int64
	fn          func(done, total int64)
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.done += int64(len(b))
	if w.fn != nil {
		w.fn(w.done, w.total)
	}
	return len(b), nil
}

// maxDownload bounds a release asset (a broken or hostile server can't
// fill the disk).
const maxDownload = 1 << 30

// download streams url into f and checks its SHA-256.
func download(ctx context.Context, url string, f *os.File, sum string, progress func(done, total int64)) error {
	resp, err := get(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	h := sha256.New()
	pw := &progressWriter{total: resp.ContentLength, fn: progress}
	n, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return err
	}
	if n > maxDownload {
		return fmt.Errorf("%s is larger than %d MB", filepath.Base(url), maxDownload>>20)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != sum {
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(url))
	}
	return nil
}

// replaceFile atomically puts src (same directory) at dst, executable.
func replaceFile(src, dst string) error {
	if err := os.Chmod(src, 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// replaceRunningExe swaps a running Windows executable: it can be renamed
// but not overwritten; the old copy is removed on the next start.
func replaceRunningExe(src, dst string) error {
	old := dst + ".old"
	_ = os.Remove(old)
	if err := os.Rename(dst, old); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		_ = os.Rename(old, dst)
		return err
	}
	return nil
}

// Cleanup removes leftovers of a previous update (Windows .old exe).
func Cleanup() {
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(exe + ".old")
	}
}

func extractTarBinary(archive, name, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s not found in the archive", name)
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeReg && filepath.Base(h.Name) == name {
			return writeFrom(dst, tr, 0o755)
		}
	}
}

func extractZipFile(archive, name, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) == name && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			return writeFrom(dst, rc, 0o755)
		}
	}
	return fmt.Errorf("%s not found in the archive", name)
}

func writeFrom(dst string, r io.Reader, mode os.FileMode) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// replaceApp unpacks lumino.app from the zip next to the installed bundle
// and swaps the two directories.
func replaceApp(archive, app string) error {
	stage, err := os.MkdirTemp(filepath.Dir(app), ".lumino-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err := unzipAll(archive, stage); err != nil {
		return err
	}
	// Lumino.app (lumino.app in the first releases)
	var fresh string
	for _, n := range []string{"Lumino.app", "lumino.app"} {
		if _, err := os.Stat(filepath.Join(stage, n, "Contents", "MacOS", "lumino")); err == nil {
			fresh = filepath.Join(stage, n)
			break
		}
	}
	if fresh == "" {
		return errors.New("Lumino.app not found in the archive")
	}
	old := filepath.Join(stage, "previous.app")
	if err := os.Rename(app, old); err != nil {
		return err
	}
	if err := os.Rename(fresh, app); err != nil {
		_ = os.Rename(old, app)
		return err
	}
	return nil
}

// unzipAll extracts a zip under dir (modes and symlinks kept, no escapes).
func unzipAll(archive, dir string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		p := filepath.Join(root, f.Name)
		if !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return fmt.Errorf("invalid path in archive: %s", f.Name)
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		case mode&os.ModeSymlink != 0:
			rc, err := f.Open()
			if err != nil {
				return err
			}
			target, err := io.ReadAll(io.LimitReader(rc, 4096))
			rc.Close()
			if err != nil {
				return err
			}
			// links must stay inside the archive: a link to an outside
			// directory followed by a file under it would write there
			t := string(target)
			if filepath.IsAbs(t) || slices.Contains(strings.Split(filepath.ToSlash(t), "/"), "..") {
				return fmt.Errorf("invalid link in archive: %s -> %s", f.Name, t)
			}
			if r := filepath.Join(filepath.Dir(p), t); r != root && !strings.HasPrefix(r, root+string(filepath.Separator)) {
				return fmt.Errorf("invalid link in archive: %s -> %s", f.Name, t)
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(t, p); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFrom(p, rc, mode.Perm()|0o200)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Restart replaces the process with the updated lumino (same arguments).
func Restart(p Plan) error {
	args := os.Args[1:]
	switch p.Kind {
	case KindMacApp:
		if err := exec.Command("open", append([]string{"-n", p.Target, "--args"}, args...)...).Start(); err != nil {
			return err
		}
		os.Exit(0)
	case KindWindows:
		cmd := exec.Command(p.Target, args...)
		if err := cmd.Start(); err != nil {
			return err
		}
		os.Exit(0)
	case KindAppImage, KindTarball:
		return syscall.Exec(p.Target, append([]string{p.Target}, args...), os.Environ())
	}
	return errors.New("restart Lumino to use the new version")
}
