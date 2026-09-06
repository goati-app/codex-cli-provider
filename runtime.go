package codexcli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const DefaultRuntimePATH = "/usr/local/bin:/usr/bin:/bin"

// RuntimePATH returns a closed PATH plus the canonical code-mode host when it
// is installed as an executable sibling of Codex.
func RuntimePATH(binary string) string {
	resolved := strings.TrimSpace(binary)
	if resolved == "" {
		return DefaultRuntimePATH
	}
	if !filepath.IsAbs(resolved) {
		found, err := exec.LookPath(resolved)
		if err != nil {
			return DefaultRuntimePATH
		}
		resolved = found
	}
	if target, err := filepath.EvalSymlinks(resolved); err == nil {
		resolved = target
	}
	dir := filepath.Dir(resolved)
	host := filepath.Join(dir, "codex-code-mode-host")
	info, err := os.Stat(host)
	if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return DefaultRuntimePATH
	}
	for _, existing := range filepath.SplitList(DefaultRuntimePATH) {
		if existing == dir {
			return DefaultRuntimePATH
		}
	}
	return dir + string(os.PathListSeparator) + DefaultRuntimePATH
}
