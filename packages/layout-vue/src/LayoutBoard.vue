<script setup lang="ts">
/**
 * <LayoutBoard>: renders a `@chadash/layout` board and edits it.
 *
 * Rendering is one flat CSS grid (see plan.ts), in the board's flow. A
 * mosaic is edited as bento: tiles stay in their sections while you move
 * them, and the mosaic comes back when editing ends. Editing never mutates
 * anything: every gesture computes boards with engine operations from the
 * board it started on, shows them as a local preview, emits them as
 * `update:board`, and emits `commit` once when the gesture ends. Engine
 * errors (a target where nothing fits, say) leave the last good preview in
 * place, so a gesture can't produce an invalid board.
 */
import {
  LayoutError,
  SIZES,
  deriveLayout,
  findItem,
  move,
  nudge,
  resize,
  sectionColumns,
  sizeLabel,
  snapSize,
  stepSize,
  type Board,
  type Breakpoint,
  type DeriveOptions,
  type Direction,
  type Item,
  type PlacedSection,
  type Section,
  type SizeId,
  type SizeStep,
} from '@chadash/layout';
import { computed, nextTick, onBeforeUnmount, onMounted, shallowRef, toRaw, useId, useTemplateRef, watch } from 'vue';
import { EDIT_SCALE, ItemMotion } from './flip.ts';
import { defaultMessages, type LayoutMessages } from './messages.ts';
import { MOMENTUM, VelocityTracker, projectMomentum, type Point } from './momentum.ts';
import { cellTarget, gridArea, neighbourOf, pitchOf, planBoard, type AreaBox, type CellTarget, type PlannedSection } from './plan.ts';
import { motionPreference, type MotionPreference } from './springs.ts';
import type { ItemSlotProps, LayoutBoardEmits, LayoutBoardProps, SectionSlotProps } from './types.ts';
import { useBreakpoint } from './useBreakpoint.ts';

defineOptions({ name: 'LayoutBoard' });

const props = withDefaults(defineProps<LayoutBoardProps>(), {
  editing: false,
  itemLabel: (item: Item) => item.id,
  motion: 'auto',
});

const emit = defineEmits<LayoutBoardEmits>();

const slots = defineSlots<{
  item(props: ItemSlotProps): unknown;
  section?(props: SectionSlotProps): unknown;
}>();

// ---------------------------------------------------------------------------
// Layout
// ---------------------------------------------------------------------------

const root = useTemplateRef<HTMLElement>('root');
const grid = useTemplateRef<HTMLElement>('grid');
const observed = useBreakpoint(root);
const breakpoint = computed<Breakpoint>(() => props.breakpoint ?? observed.breakpoint.value);
const messages = computed<LayoutMessages>(() => ({ ...defaultMessages, ...props.messages }));

/**
 * The board prop without any reactive proxy. A parent that keeps the board in
 * a deep `ref()` passes a proxy; boards are immutable, so the raw object is
 * the one to compare, derive and hand to the engine.
 */
const source = computed(() => toRaw(props.board));
/** The board shown while a gesture runs; `null` shows the prop. */
const preview = shallowRef<Board | null>(null);
const display = computed(() => preview.value ?? source.value);
const headers = computed<'auto' | 'always'>(() => (slots.section === undefined ? 'auto' : 'always'));
/** How the engine lays the board out here: the board's flow (a mosaic shows as bento while editing). */
const deriveOptions = computed<DeriveOptions>(() => {
  const flow = props.editing && display.value.flow === 'mosaic' ? 'bento' : display.value.flow;
  const base = { headers: headers.value, minRows: props.editing ? 1 : 0 };
  return flow === undefined ? base : { ...base, flow };
});
const layout = computed(() => deriveLayout(display.value, breakpoint.value, deriveOptions.value));
const plan = computed(() => planBoard(display.value, layout.value, { editing: props.editing, headers: headers.value }));

const instructionsId = `${useId()}-instructions`;

// ---------------------------------------------------------------------------
// Interaction state
// ---------------------------------------------------------------------------

interface PointerGesture {
  readonly kind: 'drag' | 'resize';
  readonly id: string;
  readonly pointerId: number;
  /** The board the gesture started from. Every preview is computed from it. */
  readonly origin: Board;
  readonly startX: number;
  readonly startY: number;
  x: number;
  y: number;
  active: boolean;
  readonly tracker: VelocityTracker;
  // drag
  readonly grabX: number;
  readonly grabY: number;
  readonly width: number;
  readonly height: number;
  target: CellTarget | undefined;
  // resize
  readonly startW: number;
  readonly startH: number;
  readonly pitch: number;
  readonly gap: number;
  readonly columns: number;
  size: SizeId;
}

interface KeyboardPick {
  readonly id: string;
  readonly origin: Board;
}

let gesture: PointerGesture | null = null;
/** Keyboard pick-up (Enter/Space). */
const picked = shallowRef<KeyboardPick | null>(null);
/** The item that's lifted: dragged, picked up or resized. Drives the `dragging` slot prop. */
const lifted = shallowRef<string | null>(null);
/** The item whose landing slot is outlined (pointer drag). */
const dropFor = shallowRef<string | null>(null);
/** The item being resized with the corner handle. */
const resizing = shallowRef<string | null>(null);
/** The roving tab stop. */
const activeId = shallowRef<string | null>(null);
const announcement = shallowRef({ seq: 0, text: '' });

/** The last board emitted, or the origin of the running gesture: echoes of it aren't external changes. */
let lastEmitted: Board | null = null;
let pendingSettle: { id: string; velocity: Point } | null = null;
let pendingFocus: string | null = null;
let refocusing = false;
/** The item `select` was last emitted for. */
let selected: string | null = null;

const elements = new Map<string, HTMLElement>();
const areas = new Map<string, HTMLElement>();

const motion = new ItemMotion((): MotionPreference => (props.motion === 'auto' ? motionPreference() : props.motion));
motion.setBaseScale(props.editing ? EDIT_SCALE : 1);

const tabStop = computed(() => {
  const id = activeId.value;
  if (id !== null && plan.value.byId.has(id)) return id;
  return plan.value.items[0]?.id;
});

const dropArea = computed(() => {
  const id = dropFor.value;
  const planned = id === null ? undefined : plan.value.byId.get(id);
  return planned === undefined ? undefined : gridArea(planned.column, planned.row, planned.placed.w, planned.tracks);
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Function ref: Vue calls it with the element on every patch and with `null` on unmount. */
function setItemRef(id: string, el: unknown): void {
  const node = el instanceof HTMLElement ? el : null;
  if (node === null) elements.delete(id);
  else elements.set(id, node);
  motion.register(id, node);
}

function setAreaRef(id: string, el: unknown): void {
  if (el instanceof HTMLElement) areas.set(id, el);
  else areas.delete(id);
}

function announce(text: string): void {
  announcement.value = { seq: announcement.value.seq + 1, text };
}

function attempt(op: () => Board): Board | undefined {
  try {
    return op();
  } catch (error) {
    if (error instanceof LayoutError) return undefined;
    throw error;
  }
}

function labelOf(id: string, board: Board = display.value): string {
  const found = findItem(board, id) ?? findItem(source.value, id);
  return found === undefined ? id : props.itemLabel(found.item);
}

function sectionName(section: PlacedSection): string {
  return props.sectionLabel?.(section) ?? (section.title !== undefined && section.title !== '' ? section.title : section.id);
}

/** "row 2, column 5", with the section when the item changed section since `from`. */
function positionText(board: Board, id: string, from?: Board): string {
  const placedLayout = deriveLayout(board, breakpoint.value, deriveOptions.value);
  for (const section of placedLayout.sections) {
    const placed = section.items.find((it) => it.id === id);
    if (placed === undefined) continue;
    const before = from === undefined ? undefined : findItem(from, id)?.section.id;
    const changed = before !== undefined && before !== section.id;
    return messages.value.position(
      changed ? { row: placed.y + 1, column: placed.x + 1, section: sectionName(section) } : { row: placed.y + 1, column: placed.x + 1 },
    );
  }
  return '';
}

function showPreview(next: Board): void {
  if (next === display.value) return;
  preview.value = next;
  lastEmitted = next;
  emit('update:board', next);
}

/** End a gesture on `final`: bring the parent to it and commit when it differs from the origin. */
function finishGesture(final: Board, origin: Board): void {
  preview.value = null;
  if (final !== lastEmitted) {
    lastEmitted = final;
    emit('update:board', final);
  }
  if (final !== origin) emit('commit', final);
}

function focusItem(id: string): void {
  activeId.value = id;
  pendingFocus = id;
  void nextTick(flushFocus);
}

function flushFocus(): void {
  const id = pendingFocus;
  pendingFocus = null;
  if (id === null) return;
  const el = elements.get(id);
  if (el === undefined || document.activeElement === el) return;
  refocusing = true;
  try {
    el.focus();
  } finally {
    refocusing = false;
  }
}

function flushSettle(): void {
  const settle = pendingSettle;
  pendingSettle = null;
  if (settle !== null) motion.play({ settle });
}

function fits(item: Item, section: Section): boolean {
  return item.supportedSizes.some((size) => SIZES[size].w <= section.span);
}

interface Geometry {
  readonly left: number;
  readonly top: number;
  readonly gap: number;
  readonly areas: readonly AreaBox[];
}

function readGeometry(): Geometry | undefined {
  const el = grid.value;
  if (el === null) return undefined;
  const rect = el.getBoundingClientRect();
  const gap = Number.parseFloat(getComputedStyle(el).columnGap) || 0;
  const boxes: AreaBox[] = [];
  for (const section of plan.value.sections) {
    const area = areas.get(section.placed.id);
    if (area === undefined) continue;
    boxes.push({
      sectionId: section.placed.id,
      left: area.offsetLeft,
      top: area.offsetTop,
      width: area.offsetWidth,
      height: area.offsetHeight,
      span: section.placed.span,
    });
  }
  return { left: rect.left, top: rect.top, gap, areas: boxes };
}

// ---------------------------------------------------------------------------
// After every layout change: FLIP, keep the dragged item under the pointer, restore focus
// ---------------------------------------------------------------------------

watch(
  [plan, observed.width],
  ([next], [previous]) => {
    const held = gesture !== null && gesture.active && gesture.kind === 'drag' ? gesture : null;
    if (next === previous) {
      // Only the container width changed: slots scaled, nothing moved.
      motion.sync();
    } else {
      const settle = pendingSettle;
      pendingSettle = null;
      motion.play({ pinned: held?.id, settle: settle ?? undefined });
    }
    if (held !== null) followPointer(held);
    flushFocus();
  },
  { flush: 'post' },
);

onMounted(() => motion.sync());

watch(
  () => props.editing,
  (editing) => {
    motion.setBaseScale(editing ? EDIT_SCALE : 1);
    if (!editing) {
      if (picked.value !== null) drop();
      if (gesture !== null) cancelPointer();
    }
  },
);

watch(breakpoint, () => {
  if (picked.value !== null) cancelPick();
  if (gesture !== null) cancelPointer();
});

watch(source, (board) => {
  const origin = gesture?.origin ?? picked.value?.origin;
  if (origin === undefined || board === lastEmitted || board === origin) return;
  // The board changed underneath a gesture (another tab, an undo): drop the gesture, emit nothing.
  const id = gesture?.active === true ? gesture.id : picked.value?.id;
  abandon();
  if (id !== undefined) announce(messages.value.interrupted(labelOf(id, board)));
});

let alive = true;
onBeforeUnmount(() => {
  alive = false;
  unlisten();
  motion.destroy();
});

// ---------------------------------------------------------------------------
// Focus and selection
// ---------------------------------------------------------------------------

function onItemFocusIn(event: FocusEvent, id: string): void {
  if (activeId.value !== id) activeId.value = id;
  const el = elements.get(id);
  const from = event.relatedTarget;
  // Focus moving inside the item, or coming back after a reorder, isn't a new selection.
  if (el !== undefined && from instanceof Node && el.contains(from)) return;
  if (refocusing && selected === id) return;
  selected = id;
  emit('select', id);
}

function onItemFocusOut(id: string): void {
  if (picked.value?.id !== id) return;
  // Wait for any focus restoration after a reorder; then a real blur drops the item.
  setTimeout(() => {
    const el = elements.get(id);
    if (alive && picked.value?.id === id && (el === undefined || !el.contains(document.activeElement))) drop();
  }, 0);
}

// ---------------------------------------------------------------------------
// Keyboard
// ---------------------------------------------------------------------------

const ARROWS: { readonly [key: string]: Direction } = {
  ArrowLeft: 'left',
  ArrowRight: 'right',
  ArrowUp: 'up',
  ArrowDown: 'down',
};

const STEPS: { readonly [K in Direction]: SizeStep } = {
  right: 'wider',
  left: 'narrower',
  down: 'taller',
  up: 'shorter',
};

function onItemKeydown(event: KeyboardEvent, id: string): void {
  if (!props.editing || gesture !== null || event.target !== event.currentTarget) return;
  if (event.altKey || event.ctrlKey || event.metaKey) return;
  const held = picked.value?.id === id;
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault();
    if (event.repeat) return;
    if (held) drop();
    else pickUp(id);
    return;
  }
  if (event.key === 'Escape') {
    if (picked.value === null) return;
    event.preventDefault();
    event.stopPropagation();
    cancelPick();
    return;
  }
  const direction = ARROWS[event.key];
  if (direction !== undefined) {
    event.preventDefault();
    if (event.shiftKey) resizeStep(id, direction);
    else if (held) nudgeStep(id, direction);
    else {
      const next = neighbourOf(plan.value, id, direction);
      if (next !== undefined) focusItem(next);
    }
    return;
  }
  if (!held && (event.key === 'Home' || event.key === 'End')) {
    event.preventDefault();
    const items = plan.value.items;
    const next = event.key === 'Home' ? items[0] : items[items.length - 1];
    if (next !== undefined) focusItem(next.id);
  }
}

function pickUp(id: string): void {
  if (picked.value !== null) drop();
  const origin = display.value;
  picked.value = { id, origin };
  lastEmitted = origin;
  lifted.value = id;
  motion.lift(id, true);
  announce(messages.value.pickedUp(labelOf(id), positionText(origin, id)));
}

function nudgeStep(id: string, direction: Direction): void {
  const from = display.value;
  const next = attempt(() => nudge(from, id, direction, { breakpoint: breakpoint.value }));
  if (next === undefined || next === from) {
    announce(messages.value.blocked(labelOf(id), direction));
    return;
  }
  showPreview(next);
  announce(messages.value.moved(labelOf(id, next), positionText(next, id, from)));
  focusItem(id);
}

function resizeStep(id: string, direction: Direction): void {
  const from = display.value;
  const planned = plan.value.byId.get(id);
  if (planned === undefined) return;
  const step = STEPS[direction];
  const columns = sectionColumns(planned.section.section, breakpoint.value);
  const size = stepSize(planned.item.supportedSizes, planned.placed.size, step, columns);
  const next = size === undefined ? undefined : attempt(() => resize(from, id, size, { breakpoint: breakpoint.value }));
  if (size === undefined || next === undefined || next === from) {
    announce(messages.value.cannotResize(labelOf(id), step));
    return;
  }
  if (picked.value?.id === id) {
    showPreview(next);
  } else {
    lastEmitted = next;
    emit('update:board', next);
    emit('commit', next);
  }
  announce(messages.value.resized(labelOf(id, next), SIZES[size].name));
  focusItem(id);
}

function drop(): void {
  const pick = picked.value;
  if (pick === null) return;
  picked.value = null;
  lifted.value = null;
  motion.lift(pick.id, false);
  const final = display.value;
  finishGesture(final, pick.origin);
  announce(messages.value.dropped(labelOf(pick.id, final), positionText(final, pick.id)));
}

function cancelPick(): void {
  const pick = picked.value;
  if (pick === null) return;
  picked.value = null;
  lifted.value = null;
  motion.lift(pick.id, false);
  finishGesture(pick.origin, pick.origin);
  announce(messages.value.cancelled(labelOf(pick.id, pick.origin), positionText(pick.origin, pick.id)));
  focusItem(pick.id);
}

/** Forget a gesture without emitting anything (the board changed underneath it). */
function abandon(): void {
  unlisten();
  const id = gesture?.id ?? picked.value?.id;
  if (gesture?.active === true && gesture.kind === 'drag') {
    // Let go of the pointer offset: the item springs back into whatever slot it now has.
    pendingSettle = { id: gesture.id, velocity: { x: 0, y: 0 } };
    void nextTick(flushSettle);
  }
  if (gesture !== null) clearResizeGhost(gesture.id);
  gesture = null;
  picked.value = null;
  preview.value = null;
  lifted.value = null;
  dropFor.value = null;
  resizing.value = null;
  if (id !== undefined) motion.lift(id, false);
}

// ---------------------------------------------------------------------------
// Pointer: drag with momentum, and the corner resize handle
// ---------------------------------------------------------------------------

function listen(): void {
  window.addEventListener('pointermove', onPointerMove);
  window.addEventListener('pointerup', onPointerUp);
  window.addEventListener('pointercancel', onPointerCancel);
  window.addEventListener('keydown', onWindowKeydown, true);
  window.addEventListener('blur', cancelPointer);
}

function unlisten(): void {
  window.removeEventListener('pointermove', onPointerMove);
  window.removeEventListener('pointerup', onPointerUp);
  window.removeEventListener('pointercancel', onPointerCancel);
  window.removeEventListener('keydown', onWindowKeydown, true);
  window.removeEventListener('blur', cancelPointer);
}

function startGesture(event: PointerEvent, partial: Pick<PointerGesture, 'kind' | 'id' | 'grabX' | 'grabY' | 'width' | 'height' | 'startW' | 'startH' | 'pitch' | 'gap' | 'columns' | 'size'>): void {
  const origin = display.value;
  lastEmitted = origin;
  gesture = {
    ...partial,
    pointerId: event.pointerId,
    origin,
    startX: event.clientX,
    startY: event.clientY,
    x: event.clientX,
    y: event.clientY,
    active: false,
    tracker: new VelocityTracker(),
    target: undefined,
  };
  gesture.tracker.add(event.timeStamp, event.clientX, event.clientY);
  listen();
}

function canStart(event: PointerEvent): boolean {
  return props.editing && gesture === null && picked.value === null && event.button === 0 && event.isPrimary !== false;
}

function onItemPointerDown(event: PointerEvent, id: string): void {
  if (!canStart(event)) return;
  const el = elements.get(id);
  const planned = plan.value.byId.get(id);
  if (el === undefined || planned === undefined) return;
  event.preventDefault();
  el.focus({ preventScroll: true });
  const rect = el.getBoundingClientRect();
  startGesture(event, {
    kind: 'drag',
    id,
    grabX: event.clientX - rect.left,
    grabY: event.clientY - rect.top,
    width: rect.width,
    height: rect.height,
    startW: planned.placed.w,
    startH: planned.placed.h,
    pitch: 0,
    gap: 0,
    columns: 0,
    size: planned.placed.size,
  });
}

function onHandlePointerDown(event: PointerEvent, id: string): void {
  event.stopPropagation();
  if (!canStart(event)) return;
  const el = elements.get(id);
  const planned = plan.value.byId.get(id);
  const geometry = readGeometry();
  const area = geometry?.areas.find((a) => a.sectionId === planned?.section.placed.id);
  if (el === undefined || planned === undefined || geometry === undefined || area === undefined) return;
  event.preventDefault();
  el.focus({ preventScroll: true });
  startGesture(event, {
    kind: 'resize',
    id,
    grabX: 0,
    grabY: 0,
    width: el.offsetWidth,
    height: el.offsetHeight,
    startW: planned.placed.w,
    startH: planned.placed.h,
    pitch: pitchOf(area, geometry.gap),
    gap: geometry.gap,
    columns: sectionColumns(planned.section.section, breakpoint.value),
    size: planned.placed.size,
  });
}

function onPointerMove(event: PointerEvent): void {
  const g = gesture;
  if (g === null || event.pointerId !== g.pointerId) return;
  g.x = event.clientX;
  g.y = event.clientY;
  g.tracker.add(event.timeStamp, g.x, g.y);
  if (!g.active) {
    if (Math.hypot(g.x - g.startX, g.y - g.startY) < MOMENTUM.dragThreshold) return;
    g.active = true;
    lifted.value = g.id;
    if (g.kind === 'drag') {
      dropFor.value = g.id;
      motion.lift(g.id, true);
    } else {
      resizing.value = g.id;
    }
  }
  if (g.kind === 'drag') dragTo(g);
  else resizeTo(g);
}

/** Hold the dragged item under the pointer, relative to its current slot. */
function followPointer(g: PointerGesture): void {
  const slot = motion.slot(g.id);
  const el = grid.value;
  if (slot === undefined || el === null) return;
  const rect = el.getBoundingClientRect();
  motion.follow(g.id, { x: g.x - g.grabX - (rect.left + slot.left), y: g.y - g.grabY - (rect.top + slot.top) });
}

function allowFor(g: PointerGesture): (sectionId: string) => boolean {
  const found = findItem(g.origin, g.id);
  if (found === undefined) return () => false;
  if (breakpoint.value !== 'desktop') return (sectionId) => sectionId === found.section.id;
  return (sectionId) => {
    const section = g.origin.sections.find((s) => s.id === sectionId);
    return section !== undefined && fits(found.item, section);
  };
}

/** The drop target for the dragged item's top-left, optionally pushed by a projection (px). */
function targetFor(g: PointerGesture, geometry: Geometry, push: Point = { x: 0, y: 0 }): CellTarget | undefined {
  const topLeft = { x: g.x - g.grabX - geometry.left + push.x, y: g.y - g.grabY - geometry.top + push.y };
  return cellTarget(geometry.areas, topLeft, { w: g.width, h: g.height }, geometry.gap, allowFor(g));
}

function moveTo(g: PointerGesture, target: CellTarget): Board | undefined {
  return attempt(() => move(g.origin, g.id, target, { breakpoint: breakpoint.value }));
}

function dragTo(g: PointerGesture): void {
  followPointer(g);
  const geometry = readGeometry();
  if (geometry === undefined) return;
  const target = targetFor(g, geometry);
  if (target === undefined) return;
  if (g.target !== undefined && g.target.sectionId === target.sectionId && g.target.x === target.x && g.target.y === target.y) return;
  g.target = target;
  const next = moveTo(g, target);
  // An engine refusal keeps the last good preview.
  if (next !== undefined) showPreview(next);
}

function resizeTo(g: PointerGesture): void {
  const dx = g.x - g.startX;
  const dy = g.y - g.startY;
  const el = elements.get(g.id);
  if (el !== undefined) {
    const min = Math.max(0, g.pitch - g.gap);
    el.style.setProperty('--layout-resize-w', `${Math.max(min, g.width + dx)}px`);
    el.style.setProperty('--layout-resize-h', `${Math.max(min, g.height + dy)}px`);
  }
  const found = findItem(g.origin, g.id);
  if (found === undefined || !(g.pitch > 0)) return;
  const size = snapSize(found.item.supportedSizes, g.startW + dx / g.pitch, g.startH + dy / g.pitch, g.columns);
  if (size === undefined || size === g.size) return;
  const next = attempt(() => resize(g.origin, g.id, size, { breakpoint: breakpoint.value }));
  if (next === undefined) return;
  g.size = size;
  showPreview(next);
}

function onPointerUp(event: PointerEvent): void {
  const g = gesture;
  if (g === null || event.pointerId !== g.pointerId) return;
  unlisten();
  gesture = null;
  if (!g.active) return;
  g.x = event.clientX;
  g.y = event.clientY;
  g.tracker.add(event.timeStamp, g.x, g.y);
  if (g.kind === 'drag') release(g, event.timeStamp);
  else endResize(g);
}

/** Drop with momentum: project the release velocity to choose the landing cell. */
function release(g: PointerGesture, time: number): void {
  const velocity = g.tracker.velocity(time);
  let final = display.value;
  const geometry = readGeometry();
  if (geometry !== undefined) {
    const sectionId = g.target?.sectionId ?? findItem(g.origin, g.id)?.section.id;
    const area = geometry.areas.find((a) => a.sectionId === sectionId) ?? geometry.areas[0];
    const push = area === undefined ? { x: 0, y: 0 } : projectMomentum(velocity, pitchOf(area, geometry.gap));
    const target = targetFor(g, geometry, push);
    const next = target === undefined ? undefined : moveTo(g, target);
    if (next !== undefined) final = next;
  }
  lifted.value = null;
  dropFor.value = null;
  motion.lift(g.id, false);
  pendingSettle = { id: g.id, velocity };
  finishGesture(final, g.origin);
  void nextTick(flushSettle);
  const label = labelOf(g.id, final);
  announce(
    final === g.origin
      ? messages.value.dropped(label, positionText(final, g.id))
      : messages.value.moved(label, positionText(final, g.id, g.origin)),
  );
}

function endResize(g: PointerGesture): void {
  clearResizeGhost(g.id);
  lifted.value = null;
  resizing.value = null;
  const final = display.value;
  finishGesture(final, g.origin);
  if (final !== g.origin) announce(messages.value.resized(labelOf(g.id, final), SIZES[g.size].name));
}

function clearResizeGhost(id: string): void {
  const el = elements.get(id);
  el?.style.removeProperty('--layout-resize-w');
  el?.style.removeProperty('--layout-resize-h');
}

function onPointerCancel(event: PointerEvent): void {
  if (gesture !== null && event.pointerId === gesture.pointerId) cancelPointer();
}

function onWindowKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Escape' || gesture === null) return;
  event.preventDefault();
  event.stopPropagation();
  cancelPointer();
}

/** Escape, pointercancel or a lost window: restore the origin board and set the item back down. */
function cancelPointer(): void {
  const g = gesture;
  unlisten();
  gesture = null;
  if (g === null || !g.active) return;
  clearResizeGhost(g.id);
  lifted.value = null;
  dropFor.value = null;
  resizing.value = null;
  motion.lift(g.id, false);
  pendingSettle = { id: g.id, velocity: { x: 0, y: 0 } };
  finishGesture(g.origin, g.origin);
  void nextTick(flushSettle);
  announce(messages.value.cancelled(labelOf(g.id, g.origin), positionText(g.origin, g.id)));
}

// ---------------------------------------------------------------------------
// Template helpers
// ---------------------------------------------------------------------------

function headerStyle(section: PlannedSection): { gridColumn: string; gridRow: string } {
  return { gridColumn: `${section.headerColumn + 1} / span ${section.headerSpan}`, gridRow: `${(section.headerRow ?? 0) + 1}` };
}

function itemName(id: string, size: SizeId): string {
  return messages.value.itemName(labelOf(id), SIZES[size].name);
}
</script>

<template>
  <div
    ref="root"
    class="layout-board"
    :data-breakpoint="breakpoint"
    :data-editing="editing || undefined"
    :role="editing ? 'group' : undefined"
    :aria-label="editing ? messages.board : undefined"
    :aria-describedby="editing ? instructionsId : undefined"
  >
    <div ref="grid" class="layout-grid" :data-flow="layout.flow" :style="{ '--layout-columns': plan.columns, gridTemplateRows: plan.rowsTemplate }">
      <template v-if="editing">
        <div
          v-for="section in plan.sections.filter((s) => layout.flow === 'sections' || s.tracks > 0)"
          :key="`area:${section.placed.id}`"
          :ref="(el) => setAreaRef(section.placed.id, el)"
          class="layout-area"
          aria-hidden="true"
          :style="gridArea(section.placed.column, section.rowStart, section.placed.span, Math.max(section.tracks, 1))"
          :data-section-area="section.placed.id"
          :data-column="section.placed.column"
          :data-span="section.placed.span"
          :data-band="section.placed.band"
          :data-rows="section.rows"
        />
      </template>
      <div v-if="dropArea" class="layout-drop" aria-hidden="true" :style="dropArea" />
      <template v-for="cell in plan.cells" :key="cell.key">
        <div
          v-if="cell.kind === 'header'"
          class="layout-section-header"
          :style="headerStyle(cell.section)"
          :data-section-id="cell.section.placed.id"
        >
          <slot name="section" :section="cell.section.placed" :editing="editing">
            <h2 class="layout-section-title">{{ sectionName(cell.section.placed) }}</h2>
          </slot>
        </div>
        <div
          v-else
          :ref="(el) => setItemRef(cell.item.id, el)"
          class="layout-item"
          :class="{
            'is-lifted': lifted === cell.item.id && resizing !== cell.item.id,
            'is-resizing': resizing === cell.item.id,
            'is-picked': picked?.id === cell.item.id,
          }"
          :style="gridArea(cell.item.column, cell.item.row, cell.item.placed.w, cell.item.tracks)"
          :data-item-id="cell.item.id"
          :data-section="cell.item.section.placed.id"
          :data-size="cell.item.placed.size"
          :data-x="cell.item.placed.x"
          :data-y="cell.item.placed.y"
          :data-w="cell.item.placed.w"
          :data-h="cell.item.placed.h"
          :data-column="cell.item.column"
          :data-band="cell.item.section.placed.band"
          :data-forced="cell.item.placed.forced || undefined"
          :tabindex="editing ? (tabStop === cell.item.id ? 0 : -1) : undefined"
          :role="editing ? 'button' : undefined"
          :aria-roledescription="editing ? messages.roleDescription : undefined"
          :aria-label="editing ? itemName(cell.item.id, cell.item.placed.size) : undefined"
          :aria-describedby="editing ? instructionsId : undefined"
          :aria-pressed="editing ? picked?.id === cell.item.id : undefined"
          @pointerdown="onItemPointerDown($event, cell.item.id)"
          @keydown="onItemKeydown($event, cell.item.id)"
          @focusin="onItemFocusIn($event, cell.item.id)"
          @focusout="onItemFocusOut(cell.item.id)"
        >
          <div class="layout-item-content" :inert="editing">
            <slot
              name="item"
              :item="cell.item.item"
              :size="cell.item.placed.size"
              :editing="editing"
              :dragging="lifted === cell.item.id"
              :placed="cell.item.placed"
            />
          </div>
          <div v-if="resizing === cell.item.id" class="layout-resize-ghost" aria-hidden="true" />
          <div v-if="editing" class="layout-handle" aria-hidden="true" @pointerdown="onHandlePointerDown($event, cell.item.id)">
            <span class="layout-size-label">{{ sizeLabel(cell.item.placed.size) }}</span>
          </div>
        </div>
      </template>
    </div>
    <p v-if="editing" :id="instructionsId" class="layout-sr">{{ messages.instructions }}</p>
    <div class="layout-sr" role="status" aria-live="polite" aria-atomic="true">
      <span :key="announcement.seq">{{ announcement.text }}</span>
    </div>
  </div>
</template>

<style>
.layout-board {
  container-type: inline-size;
  position: relative;
  min-width: 0;
}

.layout-grid {
  --layout-gap: var(--gutter, 12px);
  --layout-unit: calc((100cqi - (var(--layout-columns) - 1) * var(--layout-gap)) / var(--layout-columns));
  --layout-band-gap: max(0px, calc(var(--section-gap, 30px) - 2 * var(--layout-gap)));
  /* bento and mosaic: half a unit row, so two tracks and a gap make a unit */
  --layout-track: calc((var(--layout-unit) - var(--layout-gap)) / 2);
  position: relative;
  display: grid;
  grid-template-columns: repeat(var(--layout-columns), minmax(0, 1fr));
  grid-auto-rows: var(--layout-unit);
  gap: var(--layout-gap);
}

/* Section headers: small caps with a rule, as in the prototype. Override with the #section slot. */
.layout-section-header {
  display: flex;
  align-items: center;
  gap: var(--space-3, 12px);
  align-self: end;
  min-width: 0;
}

/* Bento and mosaic: a header has one track, so it sits in the middle of it. */
.layout-grid:not([data-flow='sections']) .layout-section-header {
  align-self: center;
}

.layout-section-header::after {
  content: '';
  flex: 1;
  min-width: var(--space-4, 16px);
  height: var(--section-rule-width, 1px);
  background: var(--section-rule-color, var(--line));
}

.layout-section-title {
  margin: 0;
  font: var(--weight-medium, 500) var(--type-micro, 10px) / 1 var(--font-num, ui-monospace, monospace);
  letter-spacing: var(--tracking-caps, 0.15em);
  text-transform: uppercase;
  white-space: nowrap;
  color: var(--text-3);
}

/* Items. Transforms belong to the motion controller; nothing here sets `transform`. */
.layout-item {
  position: relative;
  z-index: 1;
  min-width: 0;
  min-height: 0;
  border-radius: var(--tile-radius, 6px);
  outline: none;
  transform-origin: 50% 50%;
  transition: box-shadow var(--motion-press-duration, 0.3s) var(--motion-press-easing, ease);
}

.layout-item-content {
  width: 100%;
  height: 100%;
  transition: opacity var(--motion-move-duration, 0.5s) var(--motion-move-easing, ease);
}

.layout-board[data-editing] .layout-item {
  cursor: grab;
  touch-action: none;
  user-select: none;
  -webkit-user-select: none;
  box-shadow: var(--shadow-lift);
}

.layout-board[data-editing] .layout-item::after {
  content: '';
  position: absolute;
  inset: 0;
  border: var(--hairline, 1px) dashed var(--line-2);
  border-radius: inherit;
  pointer-events: none;
}

.layout-board[data-editing] .layout-item-content {
  opacity: 0.6;
}

.layout-item:focus-visible {
  box-shadow: 0 0 0 2px var(--bg), 0 0 0 4px var(--focus);
}

.layout-board[data-editing] .layout-item:focus-visible {
  box-shadow: 0 0 0 2px var(--bg), 0 0 0 4px var(--focus), var(--shadow-lift);
}

.layout-item.is-lifted,
.layout-item.is-picked,
.layout-item.is-resizing {
  z-index: 3;
  will-change: transform;
}

.layout-item.is-lifted,
.layout-item.is-picked {
  cursor: grabbing;
  box-shadow: var(--shadow-overlay);
}

.layout-item.is-lifted:focus-visible,
.layout-item.is-picked:focus-visible {
  box-shadow: 0 0 0 2px var(--bg), 0 0 0 4px var(--focus), var(--shadow-overlay);
}

.layout-board[data-editing] .layout-item.is-lifted .layout-item-content,
.layout-board[data-editing] .layout-item.is-picked .layout-item-content,
.layout-board[data-editing] .layout-item.is-resizing .layout-item-content {
  opacity: 0.85;
}

/* The unit grid, shown only while editing: one translucent cell per unit. */
.layout-area {
  z-index: 0;
  pointer-events: none;
  color: var(--text);
  background-color: var(--line);
  border-radius: var(--radius-sm, 4px);
  -webkit-mask:
    linear-gradient(90deg, currentColor var(--layout-unit), transparent 0) 0 0 / calc(var(--layout-unit) + var(--layout-gap)) 100%,
    linear-gradient(currentColor var(--layout-unit), transparent 0) 0 0 / 100% calc(var(--layout-unit) + var(--layout-gap));
  -webkit-mask-composite: source-in;
  mask:
    linear-gradient(90deg, currentColor var(--layout-unit), transparent 0) 0 0 / calc(var(--layout-unit) + var(--layout-gap)) 100%,
    linear-gradient(currentColor var(--layout-unit), transparent 0) 0 0 / 100% calc(var(--layout-unit) + var(--layout-gap));
  mask-composite: intersect;
  animation: layout-fade-in var(--motion-fade-duration, 0.15s) var(--motion-fade-easing, ease) both;
}

/* Where the dragged item will land if released now. */
.layout-drop {
  z-index: 0;
  pointer-events: none;
  border: var(--hairline, 1px) dashed var(--accent-line);
  border-radius: var(--tile-radius, 6px);
  background: var(--accent-bg);
}

/* The corner handle: the size label plus a corner glyph; drag it to resize. */
.layout-handle {
  position: absolute;
  right: var(--space-1-5, 6px);
  bottom: var(--space-1-5, 6px);
  z-index: 2;
  display: flex;
  align-items: center;
  gap: var(--space-1-5, 6px);
  padding: var(--space-0-5, 2px) var(--space-1-5, 6px);
  border: var(--hairline, 1px) solid var(--line-2);
  border-radius: var(--radius-xs, 2px);
  background: var(--surface-2);
  color: var(--text);
  cursor: nwse-resize;
  touch-action: none;
}

.layout-handle::after {
  content: '';
  width: var(--space-2, 8px);
  height: var(--space-2, 8px);
  border-right: 2px solid var(--text-2);
  border-bottom: 2px solid var(--text-2);
  border-radius: 0 0 var(--radius-xs, 2px) 0;
}

@media (pointer: coarse) {
  .layout-handle {
    min-width: var(--target-min, 44px);
    min-height: var(--target-min, 44px);
    justify-content: center;
  }
}

.layout-size-label {
  font: var(--weight-semibold, 600) var(--type-micro, 10px) / 1 var(--font-num, ui-monospace, monospace);
  letter-spacing: 0.06em;
  font-variant-numeric: tabular-nums;
}

/* The free-form outline that follows the pointer while the item snaps between sizes. */
.layout-resize-ghost {
  position: absolute;
  top: 0;
  left: 0;
  width: var(--layout-resize-w, 100%);
  height: var(--layout-resize-h, 100%);
  border: var(--hairline, 1px) dashed var(--accent);
  border-radius: var(--tile-radius, 6px);
  pointer-events: none;
}

.layout-sr {
  position: absolute;
  width: 1px;
  height: 1px;
  margin: -1px;
  padding: 0;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}

@keyframes layout-fade-in {
  from {
    opacity: 0;
  }
}
</style>
