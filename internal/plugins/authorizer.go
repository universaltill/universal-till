package plugins

import (
	"fmt"
	"log"
)

// PluginAction represents an action that requires authorization
type PluginAction string

const (
	ActionInstallPlugin   PluginAction = "plugin:install"
	ActionUpdatePlugin    PluginAction = "plugin:update"
	ActionUninstallPlugin PluginAction = "plugin:uninstall"
	ActionEnablePlugin    PluginAction = "plugin:enable"
	ActionDisablePlugin   PluginAction = "plugin:disable"
	ActionRollbackPlugin  PluginAction = "plugin:rollback"
)

// Authorizer handles RBAC checks for plugin operations
type Authorizer struct {
	requireManagerPIN bool
	logger            *log.Logger
}

// NewAuthorizer creates an authorizer with RBAC configuration
func NewAuthorizer(requireManagerPIN bool, logger *log.Logger) *Authorizer {
	return &Authorizer{
		requireManagerPIN: requireManagerPIN,
		logger:            logger,
	}
}

// CheckPermission validates if an action is authorized
func (a *Authorizer) CheckPermission(userRole string, action PluginAction) error {
	// For now, simple role-based check
	// Manager/Admin can do everything, cashier needs override for restricted actions
	restrictedActions := []PluginAction{
		ActionInstallPlugin,
		ActionUninstallPlugin,
		ActionRollbackPlugin,
	}

	isRestricted := false
	for _, ra := range restrictedActions {
		if action == ra {
			isRestricted = true
			break
		}
	}

	if isRestricted && userRole != "manager" && userRole != "admin" {
		if a.requireManagerPIN {
			return fmt.Errorf("permission denied: %s requires manager override", action)
		}
	}

	return nil
}

// AuditLog records plugin actions for compliance
func (a *Authorizer) AuditLog(action string, actor string, pluginID string, details map[string]string) {
	a.logger.Printf("[AUDIT] action=%s actor=%s plugin=%s details=%v", action, actor, pluginID, details)
}
