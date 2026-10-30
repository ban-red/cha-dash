/**
 * Shared state for a schema-driven form: what was typed for secrets (kept
 * apart from the config values), which secrets the hub already holds, the
 * errors, and which field to highlight. Provided by the editor, read by every
 * SchemaField at any depth.
 */
import { inject, type ComputedRef, type InjectionKey, type Ref } from 'vue';
import type { Errors } from '../lib/schema';
import type { TestResult } from '../model/integrations';

export interface FormContext {
  /** Typed secrets by path. Never prefilled; cleared after a save. */
  secrets: Record<string, string>;
  /** Paths of secrets the hub holds ("set"). */
  secretsSet: Ref<ReadonlySet<string>>;
  /** Paths of held, optional secrets to remove on save (sent as null). */
  cleared: Set<string>;
  errors: ComputedRef<Errors>;
  /** Show every error (after a submit), not just touched fields'. */
  showAll: Ref<boolean>;
  touched: Set<string>;
  /** The field a repair asked to focus. */
  focus: Ref<string | undefined>;
  /** Fields are read-only (the hub is unreachable). */
  disabled: Ref<boolean>;
  /** The integration the form configures, for its sign-in flow. */
  integration?: ComputedRef<{ id: string; name: string } | undefined>;
  /**
   * Test one item of a top-level list (`x-chadash-item-test`): the instance
   * with only that item in the list. Rejects with a sentence.
   */
  testItem?: (path: string, index: number) => Promise<TestResult>;
}

export const FORM: InjectionKey<FormContext> = Symbol('schema-form');

export function useForm(): FormContext {
  const f = inject(FORM);
  if (!f) throw new Error('SchemaField outside a form');
  return f;
}
