package plugins

// PluginType represents canonical plugin types per FR-017
type PluginType string

const (
	PluginTypePage             PluginType = "page"
	PluginTypeButton           PluginType = "button"
	PluginTypePopup            PluginType = "popup"
	PluginTypePayment          PluginType = "payment"
	PluginTypeDevice           PluginType = "device"
	PluginTypeIntegration      PluginType = "integration"
	PluginTypeReport           PluginType = "report"
	PluginTypePricing          PluginType = "pricing"
	PluginTypeTax              PluginType = "tax"
	PluginTypeImport           PluginType = "import"
	PluginTypeExport           PluginType = "export"
	PluginTypeHardware         PluginType = "hardware"
	PluginTypeBackgroundJob    PluginType = "background_job"
	PluginTypeScheduler        PluginType = "scheduler"
	PluginTypeReceiptTemplate  PluginType = "receipt_template"
	PluginTypeCustomerFacing   PluginType = "customer_facing"
	PluginTypeAuth             PluginType = "auth"
	PluginTypeNotification     PluginType = "notification"
	PluginTypeDelivery         PluginType = "delivery"
)

// ValidPluginTypes returns all recognized plugin types
func ValidPluginTypes() []PluginType {
	return []PluginType{
		PluginTypePage,
		PluginTypeButton,
		PluginTypePopup,
		PluginTypePayment,
		PluginTypeDevice,
		PluginTypeIntegration,
		PluginTypeReport,
		PluginTypePricing,
		PluginTypeTax,
		PluginTypeImport,
		PluginTypeExport,
		PluginTypeHardware,
		PluginTypeBackgroundJob,
		PluginTypeScheduler,
		PluginTypeReceiptTemplate,
		PluginTypeCustomerFacing,
		PluginTypeAuth,
		PluginTypeNotification,
		PluginTypeDelivery,
	}
}

// IsValid checks if a plugin type is recognized
func (pt PluginType) IsValid() bool {
	for _, valid := range ValidPluginTypes() {
		if pt == valid {
			return true
		}
	}
	return false
}

// String returns the string representation
func (pt PluginType) String() string {
	return string(pt)
}
