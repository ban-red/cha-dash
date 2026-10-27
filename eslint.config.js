// Linting for every JavaScript workspace (web/, packages/*, design/tokens),
// as create-vue sets it up: oxlint runs first and fast (.oxlintrc.json), then
// ESLint for what only it can read, chiefly Vue templates. eslint-plugin-oxlint
// turns off the ESLint rules oxlint already covers.
//
//   bun run lint
import { globalIgnores } from 'eslint/config'
import { defineConfigWithVueTs, vueTsConfigs } from '@vue/eslint-config-typescript'
import pluginVue from 'eslint-plugin-vue'
import pluginOxlint from 'eslint-plugin-oxlint'

export default defineConfigWithVueTs(
  {
    name: 'chadash/files-to-lint',
    files: ['**/*.{vue,ts,mts,tsx}'],
  },

  globalIgnores(['**/dist/**', '**/coverage/**', 'internal/webui/dist/**', 'spikes/**', 'design/prototypes/**']),

  ...pluginVue.configs['flat/essential'],
  vueTsConfigs.recommended,

  {
    name: 'chadash/house-rules',
    linterOptions: {
      // The disables that matter are oxlint's (no-control-regex, say), and
      // this config turns those rules off in ESLint, so it can't judge them.
      reportUnusedDisableDirectives: 'off',
    },
    rules: {
      // Tile, Frame, Meter: compiled <script setup> imports, never resolved
      // against HTML elements, so one word is enough.
      'vue/multi-word-component-names': 'off',
      '@typescript-eslint/no-unused-vars': [
        'error',
        { argsIgnorePattern: '^_', varsIgnorePattern: '^_', caughtErrorsIgnorePattern: '^_', ignoreRestSiblings: true },
      ],
    },
  },
  {
    name: 'chadash/tests',
    files: ['**/test/**'],
    rules: { '@typescript-eslint/no-explicit-any': 'off' },
  },

  ...pluginOxlint.buildFromOxlintConfigFile('.oxlintrc.json'),
)
