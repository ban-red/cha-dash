/**
 * The `update` attr a container carries once the Update Center checks its
 * registry (integration.ImageUpdate): read only when it's well formed.
 */
import { describe, expect, it } from 'vitest';
import { imageUpdate, type Resource } from '../src/model/types';

const container = (update: unknown): Resource => ({ id: 'docker:i_d/c/abc', kind: 'container', name: 'sonarr', source: 'integration:i_d', attrs: { update } }) as Resource;

describe('imageUpdate', () => {
  it('reads each state, with its digests, time and reason', () => {
    expect(imageUpdate(container({ state: 'available', current: 'sha256:aa', latest: 'sha256:bb', checked_at: '2026-10-31T12:00:00Z' }))).toEqual({
      state: 'available',
      current: 'sha256:aa',
      latest: 'sha256:bb',
      checked_at: '2026-10-31T12:00:00Z',
    });
    expect(imageUpdate(container({ state: 'cant_check', reason: 'The registry needs a login.' }))).toEqual({ state: 'cant_check', reason: 'The registry needs a login.' });
    expect(imageUpdate(container({ state: 'held', latest: 'sha256:cc' }))?.state).toBe('held');
  });

  it('is undefined without the attr, or when it is malformed', () => {
    expect(imageUpdate(undefined)).toBeUndefined();
    expect(imageUpdate({ ...container(undefined), attrs: {} })).toBeUndefined();
    for (const bad of ['available', ['available'], { state: 'newer' }, { latest: 'sha256:bb' }]) {
      expect(imageUpdate(container(bad))).toBeUndefined();
    }
    // A field of the wrong type is dropped, not passed on.
    expect(imageUpdate(container({ state: 'current', current: 42 }))).toEqual({ state: 'current' });
  });
});
