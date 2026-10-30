import { createApp } from 'vue';
import '@chadash/tokens/tokens.css';
import './styles/fonts.css';
import './styles/base.css';
import './ui/ui.css';
import './styles/board.css';
import './styles/tiles.css';
import './styles/chrome.css';
import App from './App.vue';
import { UiCatalog } from './dev';
import { registerServiceWorker } from './lib/pwa';
import { applyTheme, readStoredTheme } from './lib/theme';

// public/boot.js already set the attributes before first paint; this syncs
// the reactive state (and theme-color) with them.
applyTheme(readStoredTheme());

// /dev/ui on the dev server: the primitives' catalog instead of the app.
createApp(UiCatalog && location.pathname === '/dev/ui' ? UiCatalog : App).mount('#app');
registerServiceWorker();
