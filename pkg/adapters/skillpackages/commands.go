package skillpackages

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// CheckCommands locates prerequisites without executing them. Call from the
// Provider's OS identity with its environment; allowPath applies the product's
// tool filesystem policy. Finding a file does not certify runtime dependencies.
func CheckCommands(ctx context.Context, names, environment []string, workspace string, allowPath func(string) bool) error {
	values := map[string]string{}
	for _, entry := range environment {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			if runtime.GOOS == "windows" {
				k = strings.ToUpper(k)
			}
			values[k] = v
		}
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !packageCommandPattern.MatchString(name) {
			return fmt.Errorf("invalid required command: %s", name)
		}
		extensions := []string{""}
		if runtime.GOOS == "windows" {
			ext := values["PATHEXT"]
			if ext == "" {
				ext = ".COM;.EXE;.BAT;.CMD"
			}
			extensions = nil
			for _, suffix := range strings.Split(ext, ";") {
				if !strings.HasPrefix(suffix, ".") {
					continue
				}
				if strings.HasSuffix(strings.ToUpper(name), strings.ToUpper(suffix)) {
					extensions = append(extensions, "")
				}
				extensions = append(extensions, suffix)
			}
		}
		found := false
		for _, directory := range filepath.SplitList(values["PATH"]) {
			if directory == "" {
				continue
			}
			if !filepath.IsAbs(directory) {
				directory = filepath.Join(workspace, directory)
			}
			for _, ext := range extensions {
				candidate := filepath.Join(directory, name+ext)
				info, err := os.Stat(candidate)
				if err != nil || !info.Mode().IsRegular() || !commandExecutable(candidate, info) {
					continue
				}
				if allowPath != nil && !allowPath(candidate) {
					continue
				}
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			return fmt.Errorf("required command is unavailable in the Provider environment: %s", name)
		}
	}
	return nil
}
