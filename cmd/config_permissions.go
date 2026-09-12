package cmd

import (
	"errors"
	"fmt"
	"github.com/ngelik/ttsbuddy-cli/internal/api"
	"github.com/ngelik/ttsbuddy-cli/internal/config"
	"path/filepath"
	"strings"
)

const configDirPermissionsReason = "CONFIG_DIR_PERMISSIONS"

func configPermissionDetails(e *config.ConfigDirPermissionsError) map[string]any {
	return map[string]any{"path": e.Path, "actual_mode": fmt.Sprintf("%04o", e.ActualMode), "required_mode": "0700"}
}

func configPermissionAction(e *config.ConfigDirPermissionsError) *api.CLIAction {
	return &api.CLIAction{Type: "local_permission_fix", Argv: []string{"chmod", "700", e.Path}}
}

func configPermissionFix(e *config.ConfigDirPermissionsError) string {
	return "chmod 700 '" + strings.ReplaceAll(e.Path, "'", "'\"'\"'") + "'"
}

func configPermissionExit(err error) *exitError {
	var permissionErr *config.ConfigDirPermissionsError
	if !errors.As(err, &permissionErr) {
		return nil
	}
	next := "Fix: " + configPermissionFix(permissionErr) + "; then rerun: ttsbuddy doctor --json"
	mapped := structuredExitError(1, permissionErr.Error()+". "+next, "CLI_ERROR", configDirPermissionsReason, next, false, 0)
	mapped.action = configPermissionAction(permissionErr)
	payload := structuredErrorPayload(mapped)
	payload.Error.Details = configPermissionDetails(permissionErr)
	mapped.jsonPayload = payload
	return mapped
}

func checkEmailConfigDirectory() error {
	path, err := config.ConfigPath()
	if err == nil {
		err = config.CheckConfigDirPermissions(filepath.Dir(path))
	}
	if err == nil {
		return nil
	}
	if mapped := configPermissionExit(err); mapped != nil {
		return mapped
	}
	return structuredExitError(1, "authentication config directory cannot be used", "CLI_ERROR", "INVALID_CONFIGURATION", "Inspect the config directory with ttsbuddy doctor --json.", false, 0)
}
