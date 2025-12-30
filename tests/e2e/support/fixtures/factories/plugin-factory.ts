type PluginBundle = {
  plugin_id: string;
  version: string;
  checksum: string;
  merchant_id: string;
  device_id?: string;
};

type InstallIntent = {
  plugin_id: string;
  version: string;
  merchant_id: string;
  device_id?: string;
};

export class PluginFactory {
  createInstallIntent(overrides: Partial<InstallIntent> = {}): InstallIntent {
    const suffix = Date.now().toString(36);
    return {
      plugin_id: `faq-plugin-${suffix}`,
      version: '0.1.0',
      merchant_id: `merchant-${suffix}`,
      ...overrides,
    };
  }

  createBundle(overrides: Partial<PluginBundle> = {}): PluginBundle {
    const suffix = Date.now().toString(36);
    return {
      plugin_id: `faq-plugin-${suffix}`,
      version: '0.1.0',
      checksum: `sha256-${suffix}`,
      merchant_id: `merchant-${suffix}`,
      ...overrides,
    };
  }
}
