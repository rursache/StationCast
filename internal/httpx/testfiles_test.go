package httpx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Go reads a trailing _GOOS or _GOARCH in a filename as a build constraint,
// so a file named lyrics_js_test.go is only ever compiled for js/wasm and is
// silently skipped everywhere else. That is exactly what happened to the
// lyrics parser tests, which sat in the repo passing CI without ever running.
// Nothing warns about it: the file simply lands in IgnoredGoFiles
func TestNoTestFileIsSilentlyExcludedByItsName(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Skipf("cannot locate the repository root: %v", err)
	}

	// The tokens Go treats as constraints when they end a filename
	constrained := map[string]bool{}
	for _, s := range []string{
		"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos",
		"ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris",
		"wasip1", "windows", "zos",
		"386", "amd64", "arm", "arm64", "loong64", "mips", "mips64", "mips64le",
		"mipsle", "ppc64", "ppc64le", "riscv", "riscv64", "s390x", "sparc",
		"sparc64", "wasm",
	} {
		constrained[s] = true
	}

	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "node_modules" || name == "data" {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		// strip _test.go, then look at what the remaining name ends with
		stem := strings.TrimSuffix(name, "_test.go")
		if i := strings.LastIndex(stem, "_"); i >= 0 {
			if tok := stem[i+1:]; constrained[tok] {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s ends in %q, which Go reads as a build constraint, so this file never compiles on other platforms and its tests never run. Rename it", rel, tok)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func repoRoot() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
