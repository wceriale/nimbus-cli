// Package bundle packs an app directory into the .tar.gz that deploy uploads.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	ignore "github.com/sabhiram/go-gitignore"

	"github.com/wceriale/nimbus-cli/internal/output"
)

// MaxSize is the largest bundle the server accepts.
const MaxSize = 100 << 20

// alwaysSkip lists directories that are never uploaded, whatever .gitignore says.
var alwaysSkip = map[string]bool{".git": true, "node_modules": true, ".venv": true, "__pycache__": true}

// epoch is the modification time written for every file, so the same source
// always produces the same bytes, and so the same hash.
var epoch = time.Unix(0, 0)

// Bundle is a packed app directory.
type Bundle struct {
	Data   []byte
	SHA256 string
	Files  int
	// SkippedEnv lists the .env* files that were left out. Apps don't get secrets.
	SkippedEnv []string
}

type entry struct {
	rel  string // slash-separated path inside the bundle
	abs  string
	size int64
	exec bool
	link string // symlink target, if this is a symlink
}

// Create packs dir. It skips .gitignore matches, the alwaysSkip directories,
// and every .env* file.
func Create(dir string) (*Bundle, error) {
	entries, skippedEnv, err := collect(dir)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		if err := add(tw, e); err != nil {
			return nil, err
		}
		if buf.Len() > MaxSize {
			return nil, tooLarge(entries)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxSize {
		return nil, tooLarge(entries)
	}
	sum := sha256.Sum256(buf.Bytes())
	return &Bundle{Data: buf.Bytes(), SHA256: hex.EncodeToString(sum[:]), Files: len(entries), SkippedEnv: skippedEnv}, nil
}

func collect(dir string) ([]entry, []string, error) {
	var ign *ignore.GitIgnore
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err == nil {
		if ign, err = ignore.CompileIgnoreFile(filepath.Join(dir, ".gitignore")); err != nil {
			return nil, nil, err
		}
	}
	ignored := func(rel string) bool { return ign != nil && ign.MatchesPath(rel) }

	var entries []entry
	var skippedEnv []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir():
			if alwaysSkip[d.Name()] || ignored(rel) || ignored(rel+"/") {
				return filepath.SkipDir
			}
			return nil
		case strings.HasPrefix(d.Name(), ".env"):
			skippedEnv = append(skippedEnv, rel)
			return nil
		case ignored(rel):
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := entry{rel: rel, abs: p}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			if e.link, err = os.Readlink(p); err != nil {
				return err
			}
		case info.Mode().IsRegular():
			e.size, e.exec = info.Size(), info.Mode()&0o111 != 0
		default:
			return nil // sockets, devices, pipes
		}
		entries = append(entries, e)
		return nil
	})
	return entries, skippedEnv, err
}

func add(tw *tar.Writer, e entry) error {
	hdr := &tar.Header{Name: e.rel, ModTime: epoch, Mode: 0o644, Typeflag: tar.TypeReg, Size: e.size}
	if e.exec {
		hdr.Mode = 0o755
	}
	if e.link != "" {
		hdr.Typeflag, hdr.Linkname, hdr.Mode, hdr.Size = tar.TypeSymlink, e.link, 0o777, 0
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if e.link != "" {
		return nil
	}
	f, err := os.Open(e.abs)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.CopyN(tw, f, e.size)
	return err
}

// tooLarge reports the biggest top-level paths, so an agent knows what to
// add to .gitignore.
func tooLarge(entries []entry) error {
	sizes := map[string]int64{}
	for _, e := range entries {
		top, _, _ := strings.Cut(e.rel, "/")
		sizes[top] += e.size
	}
	type path struct {
		Path string  `json:"path"`
		MB   float64 `json:"mb"`
	}
	var largest []path
	for p, s := range sizes {
		largest = append(largest, path{p, math.Round(float64(s)/(1<<20)*10) / 10})
	}
	sort.Slice(largest, func(i, j int) bool {
		if largest[i].MB != largest[j].MB {
			return largest[i].MB > largest[j].MB
		}
		return largest[i].Path < largest[j].Path
	})
	if len(largest) > 5 {
		largest = largest[:5]
	}
	return &output.Error{
		Code:    "upload_too_large",
		Message: fmt.Sprintf("The app directory is over the %d MB upload limit, even compressed.", MaxSize>>20),
		Hint:    "Add paths the app doesn't need at runtime to .gitignore (details.largest lists the biggest), then run `nimbus deploy` again.",
		Details: map[string]any{"largest": largest},
	}
}
