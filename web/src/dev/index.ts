/**
 * Dev-only pieces of the app. Each export is undefined outside the Vite dev
 * server, so `vite build` drops the import and production bundles carry none
 * of this folder.
 */
import { defineAsyncComponent } from 'vue';

/** "Login as Local Dev" for the setup and sign-in doors. */
export const DevLogin = import.meta.env.DEV ? defineAsyncComponent(() => import('./DevLogin.vue')) : undefined;

/** Every UI primitive in every state, at /dev/ui (ADR-0032). */
export const UiCatalog = import.meta.env.DEV ? defineAsyncComponent(() => import('./UiCatalog.vue')) : undefined;
