/**
 * Registers the service worker (/sw.js, emitted by `vite build`). From the
 * bundle, not an inline script, so the CSP stays `default-src 'self'`.
 * Service workers need a secure context (HTTPS or localhost); on a plain LAN
 * address `navigator.serviceWorker` doesn't exist and this does nothing. Not
 * in the dev server, where there's no built worker to register.
 */
export function registerServiceWorker(): void {
  if (!import.meta.env.PROD || typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return;
  const go = () => {
    navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {
      /* blocked or failed: the app works the same, just not offline */
    });
  };
  if (document.readyState === 'complete') go();
  else window.addEventListener('load', go, { once: true });
}
