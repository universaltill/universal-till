import { test as base, expect } from '@playwright/test';
import { PluginFactory } from './factories/plugin-factory';

type TestFixtures = {
  pluginFactory: PluginFactory;
};

export const test = base.extend<TestFixtures>({
  pluginFactory: async ({}, use) => {
    const factory = new PluginFactory();
    await use(factory);
  },
});

export { expect };
