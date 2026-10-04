/**
 * A small RFC 6902 (JSON Patch) applier with RFC 6901 pointers.
 *
 * The hub only sends add, replace and remove on object members, but the
 * applier implements the whole RFC (move, copy and test too) so it stays
 * correct if the hub's patches grow.
 *
 * Patches are applied **in place**: the realtime store keeps a reactive
 * document and Vue tracks each member that changes. In-place application is
 * not atomic: if an operation fails, earlier ones in the same patch have
 * already been applied. The realtime client handles that by throwing the
 * document away and asking for a fresh snapshot (see `realtime/client.ts`).
 */

export type PatchOp =
  | { readonly op: 'add'; readonly path: string; readonly value: unknown }
  | { readonly op: 'replace'; readonly path: string; readonly value: unknown }
  | { readonly op: 'remove'; readonly path: string }
  | { readonly op: 'move'; readonly from: string; readonly path: string }
  | { readonly op: 'copy'; readonly from: string; readonly path: string }
  | { readonly op: 'test'; readonly path: string; readonly value: unknown };

export class PatchError extends Error {
  readonly index: number;
  constructor(message: string, index: number) {
    super(message);
    this.name = 'PatchError';
    this.index = index;
  }
}

type Container = Record<string, unknown> | unknown[];

/** Split an RFC 6901 pointer into unescaped reference tokens. `""` is the root. */
export function parsePointer(pointer: string): string[] {
  if (pointer === '') return [];
  if (!pointer.startsWith('/')) throw new Error(`invalid JSON pointer ${JSON.stringify(pointer)}`);
  return pointer
    .slice(1)
    .split('/')
    .map((t) => t.replace(/~1/g, '/').replace(/~0/g, '~'));
}

/** Build a pointer from raw segments (the inverse of parsePointer). */
export function toPointer(...segments: string[]): string {
  return segments.map((s) => '/' + s.replace(/~/g, '~0').replace(/\//g, '~1')).join('');
}

const isContainer = (v: unknown): v is Container => typeof v === 'object' && v !== null;
const isRecord = (v: unknown): v is Record<string, unknown> => isContainer(v) && !Array.isArray(v);

function arrayIndex(token: string, length: number, allowEnd: boolean): number {
  if (allowEnd && token === '-') return length;
  if (!/^(0|[1-9][0-9]*)$/.test(token)) throw new Error(`invalid array index ${JSON.stringify(token)}`);
  const i = Number(token);
  if (i > length || (!allowEnd && i === length)) throw new Error(`array index ${i} out of bounds`);
  return i;
}

const has = (o: Record<string, unknown>, k: string) => Object.prototype.hasOwnProperty.call(o, k);

/** Resolve a pointer to its value; throws when it doesn't exist. */
export function getAt(doc: unknown, pointer: string): unknown {
  let cur = doc;
  for (const token of parsePointer(pointer)) {
    if (Array.isArray(cur)) cur = cur[arrayIndex(token, cur.length, false)];
    else if (isRecord(cur) && has(cur, token)) cur = cur[token];
    else throw new Error(`path ${pointer} does not exist`);
  }
  return cur;
}

/** The parent container and last token of a non-root pointer. */
function parentOf(doc: unknown, pointer: string): { parent: Container; key: string } {
  const tokens = parsePointer(pointer);
  const key = tokens.pop();
  if (key === undefined) throw new Error('the root has no parent');
  let cur = doc;
  for (const token of tokens) {
    if (Array.isArray(cur)) cur = cur[arrayIndex(token, cur.length, false)];
    else if (isRecord(cur) && has(cur, token)) cur = cur[token];
    else throw new Error(`path ${pointer} does not exist`);
  }
  if (!isContainer(cur)) throw new Error(`parent of ${pointer} is not a container`);
  return { parent: cur, key };
}

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a)) return Array.isArray(b) && a.length === b.length && a.every((v, i) => deepEqual(v, b[i]));
  if (isRecord(a) && isRecord(b)) {
    const ka = Object.keys(a);
    const kb = Object.keys(b);
    return ka.length === kb.length && ka.every((k) => has(b, k) && deepEqual(a[k], b[k]));
  }
  return false;
}

const clone = <T>(v: T): T => (isContainer(v) ? (JSON.parse(JSON.stringify(v)) as T) : v);

interface Root {
  doc: unknown;
}

function add(root: Root, path: string, value: unknown): void {
  if (path === '') {
    root.doc = value;
    return;
  }
  const { parent, key } = parentOf(root.doc, path);
  if (Array.isArray(parent)) parent.splice(arrayIndex(key, parent.length, true), 0, value);
  else parent[key] = value;
}

function remove(root: Root, path: string): unknown {
  if (path === '') throw new Error('cannot remove the root');
  const { parent, key } = parentOf(root.doc, path);
  if (Array.isArray(parent)) return parent.splice(arrayIndex(key, parent.length, false), 1)[0];
  if (!has(parent, key)) throw new Error(`path ${path} does not exist`);
  const old = parent[key];
  delete parent[key];
  return old;
}

function replace(root: Root, path: string, value: unknown): void {
  if (path === '') {
    root.doc = value;
    return;
  }
  const { parent, key } = parentOf(root.doc, path);
  if (Array.isArray(parent)) parent[arrayIndex(key, parent.length, false)] = value;
  else if (has(parent, key)) parent[key] = value;
  else throw new Error(`path ${path} does not exist`);
}

/**
 * Apply `patch` to `doc` in place and return the (possibly new) root. The root
 * only changes when an operation targets `""`.
 */
export function applyPatch<T>(doc: T, patch: readonly PatchOp[]): T {
  const root: Root = { doc };
  patch.forEach((op, i) => {
    try {
      switch (op.op) {
        case 'add':
          add(root, op.path, op.value);
          break;
        case 'remove':
          remove(root, op.path);
          break;
        case 'replace':
          replace(root, op.path, op.value);
          break;
        case 'move': {
          if (op.path === op.from) break;
          if (op.path.startsWith(op.from + '/')) throw new Error(`cannot move ${op.from} into itself`);
          add(root, op.path, remove(root, op.from));
          break;
        }
        case 'copy':
          add(root, op.path, clone(getAt(root.doc, op.from)));
          break;
        case 'test':
          if (!deepEqual(getAt(root.doc, op.path), op.value)) throw new Error(`test failed at ${op.path}`);
          break;
        default:
          throw new Error(`unknown op ${JSON.stringify((op as { op: unknown }).op)}`);
      }
    } catch (err) {
      throw new PatchError(`operation ${i} (${(op as { op: string }).op}): ${(err as Error).message}`, i);
    }
  });
  return root.doc as T;
}
