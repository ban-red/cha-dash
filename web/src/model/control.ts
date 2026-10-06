/**
 * Control credentials (08 · v0.2 → Control credentials): what lets Cha Dash
 * act on an instance, as the catalog describes it.
 *
 *   catalog entry: control?: {fields?: [{field, replaces}], mode?, mode_on?, grants?, missing?}
 *   config_schema: control fields are top-level properties with "x-chadash-group": "control"
 *   recipe.control: the recipe's control variant (settings/recipe.ts shape)
 *
 * Two shapes: a second credential (Proxmox: control_token_id and
 * control_token_secret, all or nothing), or a mode on the one credential
 * (Unraid: mode = control). Without either, the instance acts with its one
 * credential or not at all, and the form shows no section.
 *
 * Pure helpers: which fields belong in the "Let Cha Dash act" section,
 * whether an instance can act now, and what a mode choice means.
 */
import type { JsonSchema } from '../lib/schema';

export interface ControlSpec {
  fields?: { field: string; replaces: string }[];
  mode?: string;
  mode_on?: string;
  grants?: string[];
  missing?: string;
}

export const GROUP_KEY = 'x-chadash-group';
export const GROUP_CONTROL = 'control';

/** Does this integration have a control credential or mode to set? */
export function hasControl(c: ControlSpec | undefined): c is ControlSpec {
  return !!c && ((c.fields?.length ?? 0) > 0 || !!c.mode);
}

/** The top-level properties the "Let Cha Dash act" section holds: the control group, and the mode. */
export function controlKeys(c: ControlSpec | undefined, schema: JsonSchema): string[] {
  if (!hasControl(c)) return [];
  const props = schema.properties ?? {};
  const keys = Object.keys(props).filter((k) => (props[k] as Record<string, unknown>)[GROUP_KEY] === GROUP_CONTROL || c.fields?.some((f) => f.field === k));
  if (c.mode && props[c.mode] && !keys.includes(c.mode)) keys.unshift(c.mode);
  return keys;
}

export type ActState = 'acts' | 'reads' | 'partial';

export interface ActStatus {
  state: ActState;
  /** Control fields still empty, for 'partial'. */
  missing: string[];
}

function filled(v: unknown): boolean {
  return v !== undefined && v !== null && v !== '';
}

/** Is the mode field set to the value that lets it act? */
export function modeOn(c: ControlSpec, config: Record<string, unknown>): boolean {
  const v = config[c.mode ?? ''];
  return c.mode_on !== undefined ? v === c.mode_on : v === true;
}

/**
 * Whether the instance can act with these settings: every control field set
 * (a secret counts when the hub holds it or one is typed), or the mode on.
 * Some control fields but not all is 'partial': the hub refuses to save it.
 */
export function actStatus(
  c: ControlSpec,
  schema: JsonSchema,
  config: Record<string, unknown>,
  secretsSet: ReadonlySet<string>,
  typed: Record<string, string> = {},
): ActStatus {
  if (c.mode) return { state: modeOn(c, config) ? 'acts' : 'reads', missing: [] };
  const props = schema.properties ?? {};
  const fields = controlKeys(c, schema);
  const missing = fields.filter((k) => (props[k]?.writeOnly ? !secretsSet.has(k) && !filled(typed[k]) : !filled(config[k])));
  if (missing.length === 0) return { state: 'acts', missing };
  return { state: missing.length === fields.length ? 'reads' : 'partial', missing };
}

/** What a mode choice means, in a sentence. */
export function modeConsequence(on: boolean, kind: string): string {
  return on
    ? `Cha Dash may act on ${kind} with this credential: each action shows a plan first, asks you to confirm by its risk, and is written to the audit log. The key needs the control scopes; the recipe asks for them.`
    : `Cha Dash only reads ${kind}: no actions on tiles, in the Inbox or in ⌘K, and no write probes. You can switch later without deleting the connection.`;
}

/** The status line for the section. */
export function actWords(s: ActStatus, kind: string): string {
  switch (s.state) {
    case 'acts':
      return `Cha Dash can act on ${kind}.`;
    case 'partial':
      return 'Half set up: fill in every control field, or clear them all.';
    default:
      return `Cha Dash only reads ${kind}.`;
  }
}
