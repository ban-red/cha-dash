/** Mapping between graph health, tile treatment, glyphs and words. */
import type { GlyphKind } from '../ui/marks/Glyph.vue';
import type { TileStatus } from '../components/Tile.vue';
import type { Health, Resource, Severity } from './types';

export function tileStatus(h: Health, r?: Resource): TileStatus {
  switch (h.status) {
    case 'attention':
      return 'attention';
    case 'down':
      return 'down';
    case 'maintenance':
      return 'maintenance';
    case 'repair':
      return 'repair';
    case 'unknown':
      return 'unknown';
    default:
      // Intentionally off is quiet, not a problem; so is paused by you (SABnzbd's `paused`).
      return r && (r.state === 'stopped' || r.state === 'off' || r.state === 'paused') ? 'off' : 'ok';
  }
}

export function glyphFor(status: TileStatus | Health['status'] | Severity): GlyphKind {
  switch (status) {
    case 'attention':
      return 'attn';
    case 'down':
      return 'down';
    case 'maintenance':
      return 'maint';
    case 'repair':
      return 'repair';
    case 'info':
      return 'prog';
    case 'off':
      return 'off';
    default:
      return 'ok';
  }
}

/** Worst of several statuses (down > attention > the rest). */
export function worst(statuses: readonly TileStatus[]): TileStatus {
  if (statuses.includes('down')) return 'down';
  if (statuses.includes('attention')) return 'attention';
  return 'ok';
}
