package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/wceriale/nimbus-cli/internal/output"
)

func writeTree(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func contents(t *testing.T, data []byte) map[string]*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = h
	}
}

func TestCreateSkips(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{
		".gitignore":              "*.log\nbuild/\n/secret.txt\ndata\n",
		"app.py":                  "print('hi')",
		"run.sh":                  "#!/bin/sh",
		"debug.log":               "noise",
		"build/out.js":            "built",
		"secret.txt":              "root only",
		"sub/secret.txt":          "anchored pattern doesn't match here",
		"data/big.csv":            "1,2,3",
		".env":                    "KEY=1",
		"config/.env.local":       "KEY=2",
		"node_modules/x/index.js": "dep",
		".git/HEAD":               "ref",
		".venv/bin/python":        "py",
		"src/__pycache__/a.pyc":   "pyc",
		"src/main.py":             "main",
	})
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	b, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := contents(t, b.Data)
	var names []string
	for n := range got {
		names = append(names, n)
	}
	slices.Sort(names)
	want := []string{".gitignore", "app.py", "run.sh", "src/main.py", "sub/secret.txt"}
	if !slices.Equal(names, want) {
		t.Errorf("bundle has %v, want %v", names, want)
	}
	if b.Files != len(want) {
		t.Errorf("Files = %d, want %d", b.Files, len(want))
	}
	if wantEnv := []string{".env", "config/.env.local"}; !slices.Equal(b.SkippedEnv, wantEnv) {
		t.Errorf("SkippedEnv = %v, want %v", b.SkippedEnv, wantEnv)
	}
	if got["run.sh"].Mode != 0o755 || got["app.py"].Mode != 0o644 {
		t.Errorf("modes: run.sh %o, app.py %o; want 755 and 644", got["run.sh"].Mode, got["app.py"].Mode)
	}
}

func TestCreateIsDeterministic(t *testing.T) {
	dir := t.TempDir()
	writeTree(t, dir, map[string]string{"a.txt": "a", "b/c.txt": "c"})
	first, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Touching files changes mtimes but not content, so the hash must hold.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	second, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 || !bytes.Equal(first.Data, second.Data) {
		t.Error("the same source produced different bundles")
	}
	writeTree(t, dir, map[string]string{"a.txt": "changed"})
	third, err := Create(dir)
	if err != nil {
		t.Fatal(err)
	}
	if third.SHA256 == first.SHA256 {
		t.Error("changed source kept the same hash")
	}
}

func TestTooLargeListsBiggestPaths(t *testing.T) {
	err := tooLarge([]entry{
		{rel: "data/a.csv", size: 300 << 20},
		{rel: "data/b.csv", size: 200 << 20},
		{rel: "models/m.bin", size: 100 << 20},
		{rel: "app.py", size: 1},
	})
	e, ok := err.(*output.Error)
	if !ok || e.Code != "upload_too_large" {
		t.Fatalf("tooLarge = %v, want upload_too_large", err)
	}
	got, _ := json.Marshal(e.Details["largest"])
	if want := `[{"path":"data","mb":500},{"path":"models","mb":100},{"path":"app.py","mb":0}]`; string(got) != want {
		t.Errorf("largest = %s, want %s", got, want)
	}
}
