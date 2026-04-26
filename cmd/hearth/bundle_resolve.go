package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// resolveBundlePath finds the .hearth bundle to use for client commands.
//
// Search order:
//  1. explicit value (the --bundle flag)
//  2. $HEARTH_BUNDLE env var
//  3. ./.hearth/admin.hearth (the coordinator's default data dir, present
//     when running CLI on the coordinator host)
//  4. ~/.hearth/admin.hearth (per-user default; copy here once)
//
// Returns a clear error listing what was tried if nothing matched.
func resolveBundlePath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("HEARTH_BUNDLE"); env != "" {
		return env, nil
	}

	tried := []string{"./.hearth/admin.hearth"}
	if exists(tried[0]) {
		return tried[0], nil
	}
	if home, err := os.UserHomeDir(); err == nil {
		userDefault := filepath.Join(home, ".hearth", "admin.hearth")
		tried = append(tried, userDefault)
		if exists(userDefault) {
			return userDefault, nil
		}
	}

	return "", fmt.Errorf(
		"no bundle found — pass --bundle, set HEARTH_BUNDLE, or place an admin bundle at one of: %v",
		tried)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, os.ErrNotExist) && err == nil
}
