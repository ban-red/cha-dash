/**
 * How bright is an icon: "dark" or "light" when its artwork would vanish on a
 * tile's plate, else "mid". Vendor logos are drawn for light pages (Frigate,
 * GitHub and Ollama are black), and some are white. Tile.vue turns the plate
 * the other way for those (tiles.css); the rest sit on the theme's own.
 *
 * It draws the loaded image small and averages the brightness of what is
 * drawn, weighted by opacity. Icons are same-origin (bundled, or served by the
 * hub), so the canvas isn't tainted; where it can't be read, the answer is
 * "mid" and the icon shows as it is.
 */
export type IconTone = 'dark' | 'light' | 'mid';

const DARK_BELOW = 0.38;
const LIGHT_ABOVE = 0.8;
const SIZE = 24;

const memo = new Map<string, IconTone>();

export function iconTone(img: HTMLImageElement): IconTone {
  const src = img.currentSrc || img.src;
  const hit = memo.get(src);
  if (hit !== undefined) return hit;
  let tone: IconTone = 'mid';
  try {
    const c = document.createElement('canvas');
    c.width = c.height = SIZE;
    const g = c.getContext('2d', { willReadFrequently: true });
    if (g) {
      g.drawImage(img, 0, 0, SIZE, SIZE);
      const px = g.getImageData(0, 0, SIZE, SIZE).data;
      let sum = 0;
      let weight = 0;
      for (let i = 0; i < px.length; i += 4) {
        const a = (px[i + 3] ?? 0) / 255;
        if (a < 0.1) continue;
        sum += a * (0.2126 * (px[i] ?? 0) + 0.7152 * (px[i + 1] ?? 0) + 0.0722 * (px[i + 2] ?? 0)) / 255;
        weight += a;
      }
      if (weight > 0) {
        const l = sum / weight;
        tone = l < DARK_BELOW ? 'dark' : l > LIGHT_ABOVE ? 'light' : 'mid';
      }
    }
  } catch {
    tone = 'mid';
  }
  if (memo.size > 500) memo.clear();
  memo.set(src, tone);
  return tone;
}
