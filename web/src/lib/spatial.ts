/**
 * Spatial focus for TV mode (plan 15, wave 34): a remote has arrow keys, not
 * Tab, so an arrow moves to the nearest tile that way. Pure, over rectangles,
 * so it's tested without a layout engine.
 */

export type Direction = 'up' | 'down' | 'left' | 'right';

export interface Box {
  x: number;
  y: number;
  w: number;
  h: number;
}

export const ARROWS: Readonly<Record<string, Direction>> = { ArrowUp: 'up', ArrowDown: 'down', ArrowLeft: 'left', ArrowRight: 'right' };

/**
 * The index of the box to move to from `from` going `dir`, or -1 when
 * there's none that way. A candidate must lie wholly past `from`'s edge in
 * that direction; among them the nearest wins, by the distance along the
 * direction plus twice the distance across it, so a box straight ahead
 * beats a nearer one off to the side.
 */
export function nextBox(boxes: readonly Box[], from: number, dir: Direction): number {
  const a = boxes[from];
  if (!a) return boxes.length ? 0 : -1;
  const ax = a.x + a.w / 2;
  const ay = a.y + a.h / 2;
  let best = -1;
  let bestScore = Infinity;
  boxes.forEach((b, i) => {
    if (i === from) return;
    const bx = b.x + b.w / 2;
    const by = b.y + b.h / 2;
    let along: number;
    let across: number;
    switch (dir) {
      case 'right':
        if (b.x < a.x + a.w - 1) return;
        along = b.x - (a.x + a.w);
        across = Math.abs(by - ay);
        break;
      case 'left':
        if (b.x + b.w > a.x + 1) return;
        along = a.x - (b.x + b.w);
        across = Math.abs(by - ay);
        break;
      case 'down':
        if (b.y < a.y + a.h - 1) return;
        along = b.y - (a.y + a.h);
        across = Math.abs(bx - ax);
        break;
      case 'up':
        if (b.y + b.h > a.y + 1) return;
        along = a.y - (b.y + b.h);
        across = Math.abs(bx - ax);
        break;
    }
    const score = Math.max(0, along) + 2 * across;
    if (score < bestScore) {
      bestScore = score;
      best = i;
    }
  });
  return best;
}
