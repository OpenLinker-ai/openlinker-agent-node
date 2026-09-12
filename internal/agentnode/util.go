package agentnode

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/OpenLinker-ai/openlinker-agent-node/pkg/adapters/appfiles"
)

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

func trustedConversationContext(value any) *ConversationContext {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var conversation ConversationContext
	if err := json.Unmarshal(raw, &conversation); err != nil {
		return nil
	}
	if conversation.Source != "core" ||
		strings.TrimSpace(conversation.SessionKey) == "" ||
		strings.TrimSpace(conversation.CurrentRunID) == "" {
		return nil
	}
	return &conversation
}

func decodeStrictJSON(raw []byte, target any) error {
	return appfiles.DecodeStrictJSON(raw, target)
}
