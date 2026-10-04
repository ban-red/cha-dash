/**
 * Shared state for a schema-driven form: what was typed for secrets (kept
 * apart from the config values), which secrets the hub already holds, the
 * errors, and which field to highlight. Provided by the editor, read by every
 * SchemaField at any depth.
 */
import { inject, type ComputedRef, type InjectionKey, type Ref } from 'vue';
import type { Errors } from '../lib/schema';

export interface FormContext {
  /** Typed secrets by path. Never prefilled; cleared after a save. */
  secrets: Record<string, string>;
  /** Paths of secrets the hub holds ("set"). */
  secretsSet: Ref<ReadonlySet<string>>;
  errors: ComputedRef<Errors>;
  /** Show every error (after a submit), not just touched fields'. */
  showAll: Ref<boolean>;
  touched: Set<string>;
  /** The field a repair asked to focus. */
  focus: Ref<string | undefined>;
  /** Fields are read-only (the hub is unreachable). */
  disabled: Ref<boolean>;
}

export const FORM: InjectionKey<FormContext> = Symbol('schema-form');

export function useForm(): FormContext {
  const f = inject(FORM);
  if (!f) throw new Error('SchemaField outside a form');
  return f;
}
