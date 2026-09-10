package providerprocess

import (
	"maps"
	"strings"
)

var baseProviderEnvironment = map[string]bool{
	"PATH": true, "Path": true, "PATHEXT": true,
	"HOME": true, "USERPROFILE": true, "HOMEDRIVE": true, "HOMEPATH": true,
	"APPDATA": true, "LOCALAPPDATA": true, "PROGRAMDATA": true,
	"SYSTEMROOT": true, "SystemRoot": true, "WINDIR": true, "COMSPEC": true,
	"TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true,
}

func Environment(environment, allowlist []string) []string {
	allowed := baseProviderEnvironment
	if len(allowlist) > 0 {
		allowed = maps.Clone(baseProviderEnvironment)
		for _, key := range allowlist {
			if key = strings.TrimSpace(key); key != "" {
				allowed[key] = true
			}
		}
	}
	result := make([]string, 0, len(environment))
	for _, item := range environment {
		key, _, ok := strings.Cut(item, "=")
		if !ok || (!allowed[key] && !strings.HasPrefix(key, "LC_")) {
			continue
		}
		result = append(result, item)
	}
	return WithIdentity(result)
}

// WithIdentity derives USER from the process's effective OS identity, never
// from caller input. Launchers that drop privileges must call this again after
// the drop. It does not expose credentials or widen the environment allowlist.
func WithIdentity(environment []string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, "USER=") {
			result = append(result, item)
		}
	}
	if name := effectiveUsername(); name != "" {
		result = append(result, "USER="+name)
	}
	return result
}
