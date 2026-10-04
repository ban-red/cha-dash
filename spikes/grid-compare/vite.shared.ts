/**
 * Resolution shared by the Vitest config and the build configs: our two
 * packages come straight from source, and every package shares one Vue.
 * (layout-vue's own dependencies resolve from this spike's node_modules.)
 */
import { fileURLToPath } from 'node:url';
import type { AliasOptions } from 'vite';

const here = (p: string) => fileURLToPath(new URL(p, import.meta.url));

export const alias: AliasOptions = {
  '@chadash/layout-vue': here('../../packages/layout-vue/src/index.ts'),
  '@chadash/layout': here('../../packages/layout/src/index.ts'),
  '@chadash/tokens/tokens.css': here('../../design/tokens/dist/tokens.css'),
};

export const dedupe = ['vue', 'motion-v', 'gridstack'];
