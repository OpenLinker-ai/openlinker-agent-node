package agentnode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func stripTrailingSlash(value string) string {
	return strings.TrimRight(value, "/")
}

func joinAPIPath(apiBase, pathName string) string {
	if strings.HasPrefix(pathName, "http://") || strings.HasPrefix(pathName, "https://") {
		return pathName
	}
	base := stripTrailingSlash(apiBase)
	if strings.HasPrefix(pathName, "/") {
		return base + pathName
	}
	return base + "/" + pathName
}

func readJSONResponse(res *http.Response) (any, error) {
	defer res.Body.Close()
	var value any
	decoder := json.NewDecoder(res.Body)
	if err := decoder.Decode(&value); err != nil {
		if errors.Is(err, http.ErrBodyReadAfterClose) {
			return JSONMap{}, nil
		}
		return JSONMap{}, nil
	}
	if value == nil {
		return JSONMap{}, nil
	}
	return value, nil
}

func boolOption(raw string, fallback bool) bool {
	if raw == "" {
		return fallback
	}
	switch strings.ToLower(raw) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func numberOption(raw string, fallback int, label string) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", label)
	}
	return value, nil
}

func parseJSONStringArray(raw, label string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var value []string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON string array: %w", label, err)
	}
	return value, nil
}

func parseJSONMap(raw, label string) (map[string]string, error) {
	if raw == "" {
		return nil, nil
	}
	var value map[string]string
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, fmt.Errorf("%s must be valid JSON object: %w", label, err)
	}
	return value, nil
}

func normalizeMetadata(value any) JSONMap {
	switch typed := value.(type) {
	case nil:
		return JSONMap{}
	case JSONMap:
		return typed
	case map[string]any:
		return JSONMap(typed)
	default:
		return JSONMap{}
	}
}

func jsonMapFromAny(value any) JSONMap {
	switch typed := value.(type) {
	case nil:
		return nil
	case JSONMap:
		return typed
	case map[string]any:
		return JSONMap(typed)
	default:
		raw, err := json.Marshal(value)
		if err != nil {
			return nil
		}
		var mapped JSONMap
		if err := json.Unmarshal(raw, &mapped); err != nil {
			return nil
		}
		return mapped
	}
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func stringFromMap(value JSONMap, key string) string {
	raw, ok := value[key]
	if !ok || raw == nil {
		return ""
	}
	return fmt.Sprint(raw)
}
