import { ChadashAdapter } from '../adapters/chadash.ts';
import { GridstackAdapter } from '../adapters/gridstack.ts';
import type { AdapterFactory } from './run.ts';

/** The engines every scenario runs against. gridstack gets its default and its two most fitting configurations. */
export const ENGINES: readonly { readonly key: string; readonly label: string; readonly make: AdapterFactory }[] = [
  { key: 'chadash', label: '@chadash/layout', make: () => new ChadashAdapter() },
  { key: 'gs-top', label: 'gridstack · top (default)', make: () => new GridstackAdapter({ mode: 'top' }) },
  { key: 'gs-top-list', label: 'gridstack · top + list columns', make: () => new GridstackAdapter({ mode: 'top', layout: 'list' }) },
  { key: 'gs-compact', label: 'gridstack · compact (v14), sections list', make: () => new GridstackAdapter({ mode: 'compact', sectionMode: 'list' }) },
];
