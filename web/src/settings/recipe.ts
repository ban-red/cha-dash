/**
 * Least-privilege recipes: how to make a credential with exactly the access
 * an integration needs. Each catalog entry may carry one (internal/integration
 * recipe.go has the contract):
 *
 *   {title, summary, access: 'read-only' | 'scoped' | 'full' | 'none',
 *    params: [{name, label, help?, default, pattern, options?, from?}],
 *    steps: [{title, blocks}], notes: [block],
 *    fills: {configField: template}, grants: [name]}
 *
 * Commands are templates: `{{name}}` is a parameter's value. A value goes
 * into a command only when it matches the parameter's anchored pattern (or is
 * one of its options), and every pattern refuses shell metacharacters, so
 * whatever is typed into the generator can't change what a command does.
 * Anything else in braces stays as it is.
 */

export type RecipeAccess = 'read-only' | 'scoped' | 'full' | 'none';

export interface RecipeOption {
  label: string;
  value: string;
  /** For a parameter bound to the form: the form field's value that picks this option. */
  when?: string;
}

export interface RecipeParam {
  name: string;
  label: string;
  help?: string;
  default: string;
  pattern?: string;
  options?: RecipeOption[];
  /** The value comes from the form instead of an input: "config.<field>", or "name" (the instance's name, as a slug). */
  from?: string;
}

export interface RecipeBlock {
  kind: 'text' | 'list' | 'code';
  text?: string;
  items?: string[];
  ordered?: boolean;
  lang?: string;
  code?: string;
}

export interface RecipeStep {
  title: string;
  blocks: RecipeBlock[];
}

export interface Recipe {
  title: string;
  summary: string;
  access: RecipeAccess | string;
  params: RecipeParam[];
  steps: RecipeStep[];
  notes: RecipeBlock[];
  fills: Record<string, string>;
  grants: string[];
}

/** The access label, in words, with the sentence that explains it. */
export const ACCESS: Record<string, { label: string; note: string; tone: 'ok' | 'attn' | 'none' }> = {
  'read-only': { label: 'Read-only', note: 'This credential can only read.', tone: 'ok' },
  scoped: { label: 'Limited', note: 'The narrowest credential this service offers can still change some things.', tone: 'attn' },
  full: { label: 'Full access only', note: 'This service has no read-only credential; Cha Dash still only reads.', tone: 'attn' },
  none: { label: 'No credential', note: 'Nothing to create.', tone: 'none' },
};

export const accessOf = (r: Recipe | undefined) => (r ? (ACCESS[r.access] ?? ACCESS['full']!) : undefined);

const PLACEHOLDER = /\{\{([a-z][a-z0-9_]*)\}\}/g;

/** The field a parameter reads from the form, if it's bound to one: a config field, or "name" for the instance's name. */
export const boundField = (p: RecipeParam) => (p.from === 'name' ? 'name' : p.from?.startsWith('config.') ? p.from.slice('config.'.length) : undefined);

/** An instance name as a slug: lowercase letters and digits, other runs become "-". */
export const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 63);

/** Does a value fit the parameter? Options by value, everything else by its anchored pattern. */
export function paramValid(p: RecipeParam, v: string): boolean {
  if (p.options?.length) return p.options.some((o) => o.value === v);
  if (!p.pattern || !p.pattern.startsWith('^') || !p.pattern.endsWith('$')) return false;
  try {
    return new RegExp(p.pattern).test(v);
  } catch {
    return false;
  }
}

/**
 * Each parameter's value: what was typed (or chosen), the form's field for a
 * bound one (trailing slashes dropped; the instance's name as a slug; for one
 * with options, the option the field's value picks), else
 * the default. Undefined when it doesn't fit: a bound field left empty, or a
 * typed value the pattern refuses.
 */
export function paramValues(recipe: Recipe, inputs: Record<string, string>, form: Record<string, unknown>, name = ''): Record<string, string | undefined> {
  const out: Record<string, string | undefined> = {};
  for (const p of recipe.params) {
    const field = boundField(p);
    let v: string;
    if (field === 'name') {
      v = slug(name);
    } else if (field && p.options?.length) {
      // The form's field picks an option (Unraid's mode picks a scope list); empty picks the default.
      const raw = form[field];
      const hit = typeof raw === 'string' && raw !== '' ? p.options.find((o) => o.when === raw) : p.options.find((o) => o.value === p.default);
      v = hit?.value ?? '';
    } else if (field) {
      const raw = form[field];
      v = typeof raw === 'string' ? raw.trim().replace(/\/+$/, '') : '';
    } else {
      v = inputs[p.name] ?? p.default;
    }
    out[p.name] = v !== '' && paramValid(p, v) ? v : undefined;
  }
  return out;
}

export type Segment = { t: 'lit'; v: string } | { t: 'val'; v: string; name: string } | { t: 'gap'; name: string; label: string };

/** A template as literal text and filled values; a gap where a value is missing or invalid. */
export function segments(tpl: string, recipe: Recipe, values: Record<string, string | undefined>): Segment[] {
  const out: Segment[] = [];
  let last = 0;
  for (const m of tpl.matchAll(PLACEHOLDER)) {
    const name = m[1]!;
    const p = recipe.params.find((x) => x.name === name);
    if (!p) continue; // not a parameter: leave it as text
    if (m.index > last) out.push({ t: 'lit', v: tpl.slice(last, m.index) });
    const v = values[name];
    out.push(v === undefined ? { t: 'gap', name, label: p.label } : { t: 'val', v, name });
    last = m.index + m[0].length;
  }
  if (last < tpl.length) out.push({ t: 'lit', v: tpl.slice(last) });
  return out;
}

/** The filled template, or undefined while a value is missing. */
export function render(tpl: string, recipe: Recipe, values: Record<string, string | undefined>): string | undefined {
  const segs = segments(tpl, recipe, values);
  if (segs.some((s) => s.t === 'gap')) return undefined;
  return segs.map((s) => (s.t === 'gap' ? '' : s.v)).join('');
}

/** The labels of the values a template still needs. */
export function gaps(tpl: string, recipe: Recipe, values: Record<string, string | undefined>): string[] {
  return [...new Set(segments(tpl, recipe, values).flatMap((s) => (s.t === 'gap' ? [s.label] : [])))];
}

export type Inline = { t: 'text' | 'code' | 'strong'; v: string };

/** The little markup recipe text allows: `code` and **bold**. Never HTML. */
export function inline(text: string): Inline[] {
  const out: Inline[] = [];
  const re = /`([^`]+)`|\*\*([^*]+)\*\*/g;
  let last = 0;
  for (const m of text.matchAll(re)) {
    if (m.index > last) out.push({ t: 'text', v: text.slice(last, m.index) });
    out.push(m[1] !== undefined ? { t: 'code', v: m[1] } : { t: 'strong', v: m[2]! });
    last = m.index + m[0].length;
  }
  if (last < text.length) out.push({ t: 'text', v: text.slice(last) });
  return out;
}

/** The config fields the recipe fills, filled; a field whose template has a gap is left out. */
export function fills(recipe: Recipe, values: Record<string, string | undefined>): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [field, tpl] of Object.entries(recipe.fills ?? {})) {
    const v = render(tpl, recipe, values);
    if (v !== undefined) out[field] = v;
  }
  return out;
}

/** Grants a test found that a credential made this way wouldn't have. */
export function extraGrants(recipe: Recipe, found: readonly { name: string }[]): string[] {
  const want = new Set(recipe.grants ?? []);
  return found.map((g) => g.name).filter((n) => !want.has(n));
}

/**
 * Put text on the clipboard. The Clipboard API needs a secure context, and
 * many hubs are opened over plain http on the LAN, so fall back to a hidden
 * textarea and the copy command there.
 */
export async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== 'undefined' && navigator.clipboard && globalThis.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    /* fall through */
  }
  if (typeof document === 'undefined') return false;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.className = 'sr';
  document.body.appendChild(ta);
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  return ok;
}
