import { MOTION } from '@chadash/tokens';
import { describe, expect, it } from 'vitest';
import { MOMENTUM, VelocityTracker, projectMomentum, projectionTime } from '../src/momentum.ts';
import { SPRINGS, springTransition } from '../src/springs.ts';
import { BREAKPOINT_MIN_WIDTHS, breakpointForWidth } from '../src/useBreakpoint.ts';

describe('springs', () => {
  it('mirror the motion tokens', () => {
    expect(SPRINGS).toEqual({ ...MOTION });
  });

  it('use the token physics exactly', () => {
    const t = springTransition('move');
    const omega = (2 * Math.PI) / 0.35;
    expect(t.stiffness).toBeCloseTo(omega * omega, 9);
    expect(t.damping).toBeCloseTo(2 * omega, 9); // bounce 0: critically damped
    expect(t.mass).toBe(1);
    const lift = springTransition('lift', 1200);
    expect(lift.damping / (2 * Math.sqrt(lift.stiffness))).toBeCloseTo(0.85, 9);
    expect(lift.velocity).toBe(1200);
  });
});

describe('momentum', () => {
  it('projects 0.2 s of travel past the dead zone', () => {
    expect(projectionTime(0.995)).toBeCloseTo(0.199, 6);
    expect(projectMomentum({ x: 200, y: 0 }, 100)).toEqual({ x: 0, y: 0 });
    expect(projectMomentum({ x: MOMENTUM.minSpeed, y: 0 }, 100)).toEqual({ x: 0, y: 0 });
    const p = projectMomentum({ x: 1250, y: 0 }, 100);
    expect(p.x).toBeCloseTo((1250 - 250) * 0.199, 6);
    expect(p.y).toBe(0);
  });

  it('keeps the direction and caps the distance at four cells', () => {
    const p = projectMomentum({ x: -6000, y: 8000 }, 90);
    expect(Math.hypot(p.x, p.y)).toBeCloseTo(4 * 90, 6);
    expect(p.x / p.y).toBeCloseTo(-6000 / 8000, 9);
  });

  it('grows with speed: a flick projects further than a slow drag', () => {
    const slow = projectMomentum({ x: 600, y: 0 }, 100).x;
    const fast = projectMomentum({ x: 1500, y: 0 }, 100).x;
    expect(fast).toBeGreaterThan(slow);
    expect(slow).toBeGreaterThan(0);
  });

  it('measures release velocity over the last 100 ms', () => {
    const tracker = new VelocityTracker();
    for (let i = 0; i <= 10; i++) tracker.add(1000 + i * 10, i * 20, i * -5);
    const v = tracker.velocity(1100);
    expect(v.x).toBeCloseTo(2000, 6);
    expect(v.y).toBeCloseTo(-500, 6);
    // Resting for longer than the window means no velocity.
    expect(tracker.velocity(1300)).toEqual({ x: 0, y: 0 });
    tracker.reset();
    expect(tracker.velocity(1100)).toEqual({ x: 0, y: 0 });
  });
});

describe('breakpoints', () => {
  it('follow the prototype container queries', () => {
    expect(breakpointForWidth(0)).toBe('phone');
    expect(breakpointForWidth(640)).toBe('phone');
    expect(breakpointForWidth(BREAKPOINT_MIN_WIDTHS.tablet)).toBe('tablet');
    expect(breakpointForWidth(1040)).toBe('tablet');
    expect(breakpointForWidth(BREAKPOINT_MIN_WIDTHS.desktop)).toBe('desktop');
    expect(breakpointForWidth(1600)).toBe('desktop');
    expect(breakpointForWidth(500, { tablet: 400, desktop: 900 })).toBe('tablet');
  });
});
