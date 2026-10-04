// Applies the remembered theme before first paint. The CSP forbids inline
// scripts, so this tiny classic script is served as a file. Keep the table in
// step with src/lib/theme.ts.
(function () {
  var defaults = {
    'instrument-dark': ['card', 'sentence'],
    'instrument-light': ['card', 'sentence'],
    oled: ['card', 'sentence'],
    night: ['card', 'sentence'],
    'high-contrast': ['card', 'sentence'],
    paper: ['ruled', 'headline'],
  };
  var id = 'instrument-dark';
  try {
    var v = window.localStorage.getItem('chadash.theme');
    if (v && Object.prototype.hasOwnProperty.call(defaults, v)) id = v;
  } catch (e) {
    /* storage blocked: use the default */
  }
  var root = document.documentElement;
  root.setAttribute('data-theme', id);
  root.setAttribute('data-surface', defaults[id][0]);
  root.setAttribute('data-density', 'comfortable');
  root.setAttribute('data-briefing', defaults[id][1]);
})();
