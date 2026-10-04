/**
 * A headless DOM for gridstack (happy-dom). Import first: gridstack reads
 * `window` and `document` when its modules load.
 */
import { GlobalRegistrator } from '@happy-dom/global-registrator';

if (typeof document === 'undefined') GlobalRegistrator.register({ width: 1280, height: 900 });
