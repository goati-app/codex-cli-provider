package codexcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimePATHFindsSiblingThroughSymlink(t *testing.T) {
	root := t.TempDir()
	release := filepath.Join(root, "release")
	links := filepath.Join(root, "links")
	if err := os.MkdirAll(release, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(links, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "codex-code-mode-host"} {
		if err := os.WriteFile(filepath.Join(release, name), []byte("binary"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(links, "codex")
	if err := os.Symlink(filepath.Join(release, "codex"), link); err != nil {
		t.Fatal(err)
	}
	if got := RuntimePATH(link); !strings.HasPrefix(got, release+string(os.PathListSeparator)) {
		t.Fatalf("PATH=%q", got)
	}
}
