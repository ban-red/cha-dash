/**
 * The JSON Schema subset that integration manifests use for their config
 * (`config_schema` in GET /api/integrations/catalog), and what the settings
 * forms are generated from:
 *
 * - types: string, number, integer, boolean, object, array (and `[T, "null"]`);
 * - `enum`, or `oneOf` of `{const, title}` for labelled choices;
 * - `required`, `default`, `title`, `description`, `examples`;
 * - string `format` (uri, hostname), `pattern`, `minLength`, `maxLength`;
 * - number `minimum`, `maximum`; array `items`, `minItems`, `maxItems`;
 * - `if` / `then` / `else` on objects (e.g. `tls.pin` only when `mode` is pin);
 * - `writeOnly: true` marks a secret. Secrets are never prefilled: whatever
 *   the hub sends for one is dropped here, and the form only says "set".
 *
 * Paths are dotted (`tls.mode`, `targets.0.url`). Values for secrets live
 * apart from the config values, keyed by path, so they can't be echoed back
 * into a config object by accident.
 */

export type JsonType = 'string' | 'number' | 'integer' | 'boolean' | 'object' | 'array' | 'null';

export interface JsonSchema {
  type?: JsonType | JsonType[];
  title?: string;
  description?: string;
  properties?: Record<string, JsonSchema>;
  required?: string[];
  items?: JsonSchema;
  enum?: readonly unknown[];
  const?: unknown;
  oneOf?: JsonSchema[];
  default?: unknown;
  format?: string;
  pattern?: string;
  minLength?: number;
  maxLength?: number;
  minimum?: number;
  maximum?: number;
  minItems?: number;
  maxItems?: number;
  writeOnly?: boolean;
  /** A secret the integration's sign-in flow can fill in (Plex's PIN): the form offers "Sign in with …". */
  'x-chadash-authorize'?: boolean;
  /** "control": part of the control credential, shown under "Let Cha Dash act" (model/control.ts). */
  'x-chadash-group'?: string;
  readOnly?: boolean;
  examples?: unknown[];
  if?: JsonSchema;
  then?: JsonSchema;
  else?: JsonSchema;
}

export type FieldKind = 'string' | 'secret' | 'number' | 'integer' | 'boolean' | 'enum' | 'object' | 'array';

export interface Choice {
  value: unknown;
  label: string;
}

export type Errors = Record<string, string>;

/** The main type, ignoring "null" in a union. */
export function typeOf(s: JsonSchema): JsonType | undefined {
  if (Array.isArray(s.type)) return s.type.find((t) => t !== 'null');
  if (s.type) return s.type;
  if (s.properties) return 'object';
  if (s.items) return 'array';
  if (s.enum || s.oneOf) return typeof (s.enum?.[0] ?? s.oneOf?.[0]?.const) === 'number' ? 'number' : 'string';
  return undefined;
}

/** Labelled choices for `enum` or `oneOf: [{const, title}]`. */
export function choices(s: JsonSchema): Choice[] | undefined {
  // The hub sends both for labelled choices (enum to validate, oneOf for the titles): prefer the titles.
  if (s.oneOf?.length && s.oneOf.every((o) => 'const' in o)) return s.oneOf.map((o) => ({ value: o.const, label: o.title ?? String(o.const) }));
  if (s.enum) return s.enum.map((v) => ({ value: v, label: String(v) }));
  return undefined;
}

export function kindOf(s: JsonSchema): FieldKind {
  if (s.writeOnly) return 'secret';
  if (choices(s)) return 'enum';
  const t = typeOf(s);
  if (t === 'number' || t === 'integer' || t === 'boolean' || t === 'object' || t === 'array') return t;
  return 'string';
}

/** Words for a field: its title, or its key made readable ("api_key" → "API key"). */
export function fieldLabel(key: string, s: JsonSchema): string {
  if (s.title) return s.title;
  const words = key.replace(/([a-z])([A-Z])/g, '$1 $2').split(/[_\s-]+/).filter(Boolean);
  const ACRONYMS: Record<string, string> = { url: 'URL', api: 'API', id: 'ID', tls: 'TLS', ip: 'IP', http: 'HTTP', tcp: 'TCP', ssl: 'SSL', uri: 'URI' };
  const out = words.map((w, i) => ACRONYMS[w.toLowerCase()] ?? (i === 0 ? w[0]!.toUpperCase() + w.slice(1) : w.toLowerCase()));
  return out.join(' ') || key;
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v);

/** Does `value` satisfy `s`? (Used for `if`; a missing value never matches a `const`.) */
export function matches(s: JsonSchema, value: unknown): boolean {
  const errors: Errors = {};
  validateNode(s, value, '', { secretsSet: new Set(), secrets: {} }, errors);
  return Object.keys(errors).length === 0;
}

/**
 * The object schema with its active `if`/`then`/`else` branch folded in, so a
 * field that only applies in one branch (a TLS pin) appears and is required
 * only when it applies.
 */
export function effective(s: JsonSchema, value: unknown): JsonSchema {
  if (!s.if || (!s.then && !s.else)) return s;
  const branch = matches(s.if, value ?? {}) ? s.then : s.else;
  if (!branch) return s;
  return {
    ...s,
    properties: { ...s.properties, ...branch.properties },
    required: [...new Set([...(s.required ?? []), ...(branch.required ?? [])])],
  };
}

/**
 * The form's starting values: `existing` config where it fits the schema,
 * else each field's default. Secrets are dropped at every depth, whatever the
 * hub sent for them.
 */
export function initialValue(s: JsonSchema, existing?: unknown): unknown {
  if (s.writeOnly) return undefined;
  const t = typeOf(s);
  if (t === 'object') {
    const src = isObj(existing) ? existing : undefined;
    const out: Record<string, unknown> = {};
    const props = { ...s.properties, ...s.then?.properties, ...s.else?.properties };
    for (const [k, p] of Object.entries(props)) {
      if (p.writeOnly) continue;
      const v = initialValue(p, src?.[k]);
      if (v !== undefined) out[k] = v;
    }
    return out;
  }
  if (t === 'array') {
    const src = Array.isArray(existing) ? existing : Array.isArray(s.default) ? s.default : [];
    return s.items ? src.map((v) => initialValue(s.items!, v)) : [...src];
  }
  if (existing !== undefined && existing !== null && !isObj(existing)) return existing;
  if (s.const !== undefined) return s.const;
  return s.default;
}

/** A fresh item for an array field ("Add target"). */
export function newItem(s: JsonSchema): unknown {
  return s.items ? initialValue(s.items) : undefined;
}

export interface ValidateOptions {
  /** Paths of secrets the hub already holds: blank means "keep it". */
  secretsSet: ReadonlySet<string>;
  /** What the person typed for secrets, by path. */
  secrets: Readonly<Record<string, string>>;
}

/** Plain-words problems with the form, by path. Empty means it's good to send. */
export function validate(s: JsonSchema, value: unknown, opts: ValidateOptions): Errors {
  const errors: Errors = {};
  validateNode(s, value, '', opts, errors);
  return errors;
}

const join = (base: string, key: string | number) => (base ? `${base}.${key}` : String(key));
const blank = (v: unknown) => v === undefined || v === null || (typeof v === 'string' && v.trim() === '');
const example = (s: JsonSchema) => (s.examples?.length ? String(s.examples[0]) : undefined);

function validateNode(s0: JsonSchema, value: unknown, path: string, opts: ValidateOptions, errors: Errors): void {
  const t = typeOf(s0);
  if (t === 'object') {
    const obj = isObj(value) ? value : {};
    const s = effective(s0, obj);
    const required = new Set(s.required ?? []);
    for (const [k, p] of Object.entries(s.properties ?? {})) {
      const at = join(path, k);
      if (p.readOnly) continue;
      if (p.writeOnly) {
        const typed = opts.secrets[at] ?? '';
        if (blank(typed)) {
          if (required.has(k) && !opts.secretsSet.has(at)) errors[at] = 'Required.';
        } else {
          checkString(p, typed, at, errors);
        }
        continue;
      }
      const v = obj[k];
      if (blank(v) || (typeOf(p) === 'array' && Array.isArray(v) && v.length === 0 && !p.minItems)) {
        if (required.has(k) && typeOf(p) !== 'boolean' && typeOf(p) !== 'object') errors[at] = 'Required.';
        if (typeOf(p) === 'object' && (required.has(k) || isObj(v))) validateNode(p, v ?? {}, at, opts, errors);
        continue;
      }
      validateNode(p, v, at, opts, errors);
    }
    return;
  }
  if (blank(value)) return;
  const options = choices(s0);
  if (options) {
    if (!options.some((c) => c.value === value)) errors[path] = 'Pick one of the options.';
    return;
  }
  if (s0.const !== undefined && value !== s0.const) {
    errors[path] = 'Doesn’t match.';
    return;
  }
  if (t === 'array') {
    const arr = Array.isArray(value) ? value : [];
    if (s0.minItems !== undefined && arr.length < s0.minItems) errors[path] = `Add at least ${s0.minItems}.`;
    if (s0.maxItems !== undefined && arr.length > s0.maxItems) errors[path] = `At most ${s0.maxItems}.`;
    if (s0.items) arr.forEach((v, i) => validateNode(s0.items!, v, join(path, i), opts, errors));
    return;
  }
  if (t === 'number' || t === 'integer') {
    if (typeof value !== 'number' || !Number.isFinite(value)) {
      errors[path] = t === 'integer' ? 'Enter a whole number.' : 'Enter a number.';
      return;
    }
    if (t === 'integer' && !Number.isInteger(value)) {
      errors[path] = 'Enter a whole number.';
      return;
    }
    const { minimum: lo, maximum: hi } = s0;
    if ((lo !== undefined && value < lo) || (hi !== undefined && value > hi)) {
      errors[path] = lo !== undefined && hi !== undefined ? `Between ${lo} and ${hi}.` : lo !== undefined ? `At least ${lo}.` : `At most ${hi}.`;
    }
    return;
  }
  if (t === 'boolean') {
    if (typeof value !== 'boolean') errors[path] = 'Choose yes or no.';
    return;
  }
  if (typeof value !== 'string') {
    errors[path] = 'Enter text.';
    return;
  }
  checkString(s0, value, path, errors);
}

function checkString(s: JsonSchema, v: string, path: string, errors: Errors): void {
  if (s.minLength !== undefined && [...v].length < s.minLength) {
    errors[path] = `At least ${s.minLength} characters.`;
    return;
  }
  if (s.maxLength !== undefined && [...v].length > s.maxLength) {
    errors[path] = `At most ${s.maxLength} characters.`;
    return;
  }
  if ((s.format === 'uri' || s.format === 'url') && !isUri(v)) {
    const ex = example(s);
    errors[path] = ex ? `Enter a full URL, like ${ex}` : 'Enter a full URL, with http:// or https://';
    return;
  }
  if (s.format === 'hostname' && !/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$/i.test(v)) {
    errors[path] = 'Enter a host name, like nas.home.arpa';
    return;
  }
  if (s.pattern) {
    let re: RegExp | undefined;
    try {
      re = new RegExp(s.pattern, 'u');
    } catch {
      re = undefined; // a pattern this browser can't compile: the hub still checks it
    }
    if (re && !re.test(v)) {
      const ex = example(s);
      errors[path] = ex ? `That doesn’t look right. Example: ${ex}` : 'That doesn’t look right.';
    }
  }
}

function isUri(v: string): boolean {
  try {
    const u = new URL(v);
    return /^[a-z][a-z0-9+.-]*:$/i.test(u.protocol) && (u.host !== '' || u.protocol === 'unix:' || u.pathname.length > 1);
  } catch {
    return false;
  }
}

/**
 * Ready to send: blank optional values dropped (the hub applies its own
 * defaults), fields of inactive branches left out, secrets left out.
 */
export function clean(s: JsonSchema, value: unknown): unknown {
  const t = typeOf(s);
  if (t === 'object') {
    const obj = isObj(value) ? value : {};
    const eff = effective(s, obj);
    const out: Record<string, unknown> = {};
    for (const [k, p] of Object.entries(eff.properties ?? {})) {
      if (p.writeOnly || p.readOnly) continue;
      const v = clean(p, obj[k]);
      if (v === undefined) continue;
      if (isObj(v) && Object.keys(v).length === 0 && !(eff.required ?? []).includes(k)) continue;
      out[k] = v;
    }
    return out;
  }
  if (t === 'array') {
    if (!Array.isArray(value)) return undefined;
    return s.items ? value.map((v) => clean(s.items!, v)).filter((v) => v !== undefined) : value;
  }
  if (blank(value)) return undefined;
  return typeof value === 'string' ? value.trim() : value;
}

/** Top-level and nested secrets, as `{path: value}`, only the ones typed. */
export function typedSecrets(secrets: Readonly<Record<string, string>>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(secrets)) if (v !== '') out[k] = v;
  return out;
}

/** Paths of every secret in the schema, in form order. */
export function secretPaths(s: JsonSchema, base = ''): string[] {
  if (typeOf(s) !== 'object') return [];
  const out: string[] = [];
  const props = { ...s.properties, ...s.then?.properties, ...s.else?.properties };
  for (const [k, p] of Object.entries(props)) {
    const at = join(base, k);
    if (p.writeOnly) out.push(at);
    else if (typeOf(p) === 'object') out.push(...secretPaths(p, at));
  }
  return out;
}

/** Paths of every field, in form order (for focusing the first one, or a named one). */
export function fieldPaths(s: JsonSchema, base = ''): string[] {
  if (typeOf(s) !== 'object') return [];
  const out: string[] = [];
  for (const [k, p] of Object.entries(s.properties ?? {})) {
    const at = join(base, k);
    if (typeOf(p) === 'object' && !p.writeOnly) out.push(...fieldPaths(p, at));
    else out.push(at);
  }
  return out;
}

/** A DOM id for a field path. */
export const fieldId = (path: string) => `f-${path.replace(/[^A-Za-z0-9_-]/g, '-')}`;
