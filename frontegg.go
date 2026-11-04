package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// fronteggTokenStatus reads the Frontegg JWT saved in path and reports how
// long it is still valid. The file may hold the bare JWT, JSON
// {"token":"<jwt>"}, or an "Authorization: Bearer <jwt>" line. It fails if the
// file is missing, empty or not a JWT, or if the token has expired.
func fronteggTokenStatus(path string, now time.Time) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s not readable: %w", path, err)
	}
	token := strings.TrimSpace(string(data))
	if strings.HasPrefix(token, "{") {
		var wrapped struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal([]byte(token), &wrapped); err != nil {
			return "", fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
		token = strings.TrimSpace(wrapped.Token)
	}
	token = strings.TrimSpace(strings.TrimPrefix(token, "Authorization:"))
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer"))
	if token == "" {
		return "", errors.New(path + " is empty")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New(path + " does not hold a JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", fmt.Errorf("%s JWT payload is not base64: %w", path, err)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp == 0 {
		return "", errors.New(path + " JWT has no exp claim")
	}
	left := time.Unix(claims.Exp, 0).Sub(now)
	if left <= 0 {
		return "", fmt.Errorf("token expired %s ago", (-left).Round(time.Minute))
	}
	return fmt.Sprintf("token valid for another %s", left.Round(time.Minute)), nil
}
