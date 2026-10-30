/**
 * A head start for the add form: what discovery found (a URL, its TLS mode
 * and a name), handed from the wizard to Settings → Integrations → Add. Taken
 * once, by the first add form for that integration. Only a key the person
 * already put in a check's URL travels (`key`), and only in memory.
 */

export interface Draft {
  name?: string;
  config: Record<string, unknown>;
  /** A key found in a check's URL, for the integration's first secret field. */
  key?: string;
}

const drafts = new Map<string, Draft>();

export function setDraft(integration: string, draft: Draft): void {
  // A plain copy: the config may be a reactive proxy, which structuredClone refuses.
  drafts.set(integration, { ...draft, config: JSON.parse(JSON.stringify(draft.config)) as Record<string, unknown> });
}

/** The draft for an integration, removed as it's taken. */
export function takeDraft(integration: string): Draft | undefined {
  const d = drafts.get(integration);
  drafts.delete(integration);
  return d;
}
