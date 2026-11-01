import { afterEach, describe, expect, it, vi } from 'vitest';
import { dozzleBase, dozzleLink, loadDozzle } from '../src/lib/dozzle';

afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('Dozzle links', () => {
  it('links a container by its short ID', () => {
    expect(dozzleLink('https://logs.home.example', '3f1a2b3c4d5e')).toBe('https://logs.home.example/container/3f1a2b3c4d5e');
    expect(dozzleLink('http://10.20.30.5:8080/dozzle/', '3f1a2b3c4d5e6f7a8b9c')).toBe('http://10.20.30.5:8080/dozzle/container/3f1a2b3c4d5e');
  });

  it('links nothing without an address or a container ID', () => {
    expect(dozzleLink('', '3f1a2b3c4d5e')).toBeUndefined();
    expect(dozzleLink('javascript:alert(1)', '3f1a2b3c4d5e')).toBeUndefined();
    expect(dozzleLink('https://logs.home.example', undefined)).toBeUndefined();
    expect(dozzleLink('https://logs.home.example', '../settings')).toBeUndefined();
    expect(dozzleLink('https://logs.home.example', 'abc')).toBeUndefined();
  });

  it('reads the address once, and leaves it unset when the hub refuses', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(200, { url: 'https://logs.home.example' }));
    vi.stubGlobal('fetch', fetchMock);
    await loadDozzle(true);
    await loadDozzle();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/settings/dozzle');
    expect(dozzleBase.value).toBe('https://logs.home.example');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(json(403, { error: { code: 'forbidden', message: 'Household accounts can’t open this.' } })));
    await loadDozzle(true);
    expect(dozzleBase.value).toBe('');
  });
});
