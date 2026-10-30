/**
 * The Home Assistant tile (widgets/HomeAssistantWidget.vue): presence, the
 * facets, the People, Climate and Playing views, and the house power
 * setting (`options.power`), off by default.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, defineComponent, h as el } from 'vue';
import type { SizeId } from '@chadash/layout';
import HomeAssistantWidget from '../src/widgets/HomeAssistantWidget.vue';
import { WIDGETS } from '../src/model/widgets';
import { optionValue } from '../src/widgets/options';
import { resetViews } from '../src/widgets/views';
import { setDisplayTimeZone } from '../src/lib/format';
import { provideTile, type TileConfig } from '../src/stores/tiles';
import { axeViolations } from './axe';
import { docs, healthy } from './fixtures';
import { harness } from './harness';

beforeEach(() => {
  setDisplayTimeZone('UTC');
  localStorage.clear();
  resetViews();
});
afterEach(() => {
  vi.useRealTimers();
  document.body.innerHTML = '';
});

/** The Demo Home, with what the integration reads from a fuller house. */
function fullerHouse(tripped = false) {
  const d = docs(healthy);
  const base = d.metrics.series['app:homeassistant#people_home']!;
  const put = (k: string, value: number) => (d.metrics.series[`app:homeassistant#${k}`] = { ...base, value });
  put('people_home', 2);
  put('safety_tripped', tripped ? 1 : 0);
  put('media_playing', 1);
  put('open', 0);
  put('locks_unlocked', 0);
  const app = d.graph.resources['app:homeassistant']!;
  app.attrs = {
    ...app.attrs,
    people_total: 3,
    temperature_unit: '°C',
    people_items: [
      { name: 'Sam', where: 'home', since: '2026-10-03T08:00:00Z' },
      { name: 'Robin', where: 'Work', since: '2026-10-03T09:12:00Z' },
      { name: 'Jo', where: 'home', since: '2026-10-03T07:30:00Z' },
    ],
    climate_items: [
      { name: 'Living room', current: 21.5, target: 22, action: 'heating', humidity: 47 },
      { name: 'Bedroom', current: 18.9, target: 17, action: 'idle' },
    ],
    safety_total: 3,
    safety_items: tripped ? [{ name: 'Utility room leak', kind: 'moisture' }] : [],
    media_total: 3,
    media_items: [{ name: 'Living room speaker', title: 'Weightless', artist: 'Marconi Union' }],
    locks_total: 2,
    open_items: [],
    battery_items: [],
    update_items: [],
  };
  return d;
}

function mountHa(size: SizeId, config?: TileConfig, data = docs(healthy)) {
  const hs = harness(data);
  const Host = defineComponent({
    setup() {
      provideTile({ itemId: 'homeassistant@app:homeassistant', type: 'homeassistant', target: 'app:homeassistant', config: computed(() => config) });
      return () => el('main', [el(HomeAssistantWidget, { size, target: 'app:homeassistant' })]);
    },
  });
  return hs.mountIn(Host);
}
const labels = (w: ReturnType<typeof mountHa>) => w.findAll('.ha-facet dt').map((d) => d.text());

describe('Home Assistant presence and facets', () => {
  it('marks each person, home first, and says who is home and where the others are', () => {
    const w = mountHa('wide', undefined, fullerHouse());
    expect(w.findAll('.ha-pres .ha-face').map((f) => [f.text(), f.classes().includes('home')])).toEqual([
      ['J', true],
      ['S', true],
      ['R', false],
    ]);
    expect(w.find('.ha-names').text()).toBe('Jo, Sam home');
    expect(w.find('.ha-else').text()).toBe('Robin at Work');
  });

  it('says everyone or nobody is home', () => {
    const d = fullerHouse();
    const app = d.graph.resources['app:homeassistant']!;
    app.attrs = { ...app.attrs, people_items: [{ name: 'Sam', where: 'home' }, { name: 'Jo', where: 'home' }] };
    expect(mountHa('wide', undefined, d).find('.ha-names').text()).toBe('Everyone’s home');
    app.attrs = { ...app.attrs, people_items: [{ name: 'Sam', where: 'Gym' }, { name: 'Jo', where: 'away' }] };
    const w = mountHa('wide', undefined, d);
    expect(w.find('.ha-names').text()).toBe('Nobody’s home');
    expect(w.find('.ha-else').text()).toBe('Jo away · Sam at Gym');
  });

  it('shows four facets on a wide tile and six on a large, what is playing ahead of quiet sensors', () => {
    const w = mountHa('wide', undefined, fullerHouse());
    expect(labels(w)).toEqual(['Indoors', 'Lights', 'Playing', 'Safety']);
    const text = w.findAll('.ha-facet').map((f) => f.text());
    expect(text[0]).toBe('Indoors20.2°CLiving room heating');
    expect(text[2]).toBe('Playing1Weightless · Marconi Union');
    expect(text[3]).toBe('SafetyOK3 sensors');
    expect(labels(mountHa('large', undefined, fullerHouse()))).toEqual(['Indoors', 'Lights', 'Playing', 'Safety', 'Doors']);
  });

  it('moves a tripped sensor to the front and marks the tile', () => {
    const w = mountHa('wide', undefined, fullerHouse(true));
    const first = w.find('.ha-facet');
    expect(first.find('dt').text()).toBe('Safety');
    expect(first.classes()).toContain('flag');
    expect(first.text()).toContain('Utility room leak · water leak');
    expect(w.text()).toContain('Safety alert');
    expect(w.find('.tf').text()).toBe('Needs a look: 1 safety alert');
  });

  it('flags open doors and unlocked locks, naming a lone open door', () => {
    const d = fullerHouse();
    const base = d.metrics.series['app:homeassistant#people_home']!;
    d.metrics.series['app:homeassistant#open'] = { ...base, value: 1 };
    d.graph.resources['app:homeassistant']!.attrs!['open_items'] = [{ name: 'Back door', kind: 'door' }];
    const w = mountHa('wide', undefined, d);
    expect(w.find('.ha-facet').text()).toBe('Doors1Back door open');
    d.metrics.series['app:homeassistant#locks_unlocked'] = { ...base, value: 1 };
    expect(mountHa('wide', undefined, d).find('.ha-facet').text()).toBe('Doors21 open · 1 unlocked');
  });

  it('leaves out a facet the house has no sensors for', () => {
    const d = fullerHouse();
    d.graph.resources['app:homeassistant']!.attrs = { ...d.graph.resources['app:homeassistant']!.attrs, climate_items: [], media_total: 0 };
    expect(labels(mountHa('wide', undefined, d))).toEqual(['Lights', 'Safety', 'Doors']);
  });

  it('names the header after the alarm', () => {
    const d = fullerHouse();
    d.graph.resources['app:homeassistant']!.attrs!['alarm'] = 'armed_home';
    expect(mountHa('wide', undefined, d).find('.tg').text()).toContain('alarm armed home');
  });
});

describe('Home Assistant views', () => {
  it('offers People, Climate and Playing beside Now', () => {
    const w = mountHa('wide', undefined, fullerHouse());
    expect(w.findAll('.tile-views button').map((b) => b.attributes('aria-label'))).toEqual(['Now', 'People', 'Climate', 'Playing']);
  });

  it('lists where each person is, and since when', async () => {
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-10-03T14:05:10Z'));
    const w = mountHa('wide', undefined, fullerHouse());
    await w.findAll('.tile-views button')[1]!.trigger('click');
    expect(w.findAll('[aria-label="People in Home Assistant"] [role="listitem"]').map((r) => r.text())).toEqual([
      'JJosince 07:30Home',
      'SSamsince 08:00Home',
      'RRobinsince 09:12Work',
    ]);
  });

  it('lists each thermostat with what it is doing', async () => {
    const w = mountHa('wide', undefined, fullerHouse());
    await w.findAll('.tile-views button')[2]!.trigger('click');
    expect(w.findAll('[aria-label="Climate in Home Assistant"] [role="listitem"]').map((r) => r.text())).toEqual([
      'Living roomheating to 22.0° · 47% humidity21.5°C',
      'Bedroomidle · set 17.0°18.9°C',
    ]);
  });

  it('lists what each player plays', async () => {
    const w = mountHa('small', undefined, fullerHouse());
    await w.findAll('.tile-views button').at(-1)!.trigger('click');
    expect(w.find('[aria-label="Playing in Home Assistant"]').text()).toBe('WeightlessMarconi Union · Living room speaker');
  });

  it('shows climate and playing as sections on a large tile, with no views', () => {
    const w = mountHa('large', undefined, fullerHouse());
    expect(w.find('.tile-views').exists()).toBe(false);
    expect(w.findAll('.ha-sections > .ha-k').map((h) => h.text())).toEqual(['Climate', 'Playing']);
    expect(w.text()).toContain('All closed, locked and up to date.');
  });

  it('passes axe on every view and the 4×4', async () => {
    for (const size of ['small', 'wide'] as const) {
      const w = mountHa(size, undefined, fullerHouse(true));
      const dots = w.findAll('.tile-views button');
      for (const [i, d] of dots.entries()) {
        await d.trigger('click');
        expect(await axeViolations(), `${size} view ${i}`).toEqual([]);
      }
      w.unmount();
    }
    mountHa('large', undefined, fullerHouse(true));
    expect(await axeViolations(), 'large').toEqual([]);
  });
});

describe('Home Assistant house power', () => {
  it('is off unless turned on', () => {
    const [power] = WIDGETS['homeassistant']!.options!;
    expect(optionValue(power!, undefined)).toBe(false);
    expect(optionValue(power!, true)).toBe(true);
  });

  it('shows no power by default', () => {
    for (const size of ['strip', 'small', 'wide', 'large'] as const) {
      const w = mountHa(size);
      expect(w.text(), size).not.toMatch(/kW|kWh/);
      w.unmount();
    }
  });

  it('leads the facets with power and energy today, beside the graph, when turned on', () => {
    const wide = mountHa('wide', { options: { power: true } });
    expect(wide.find('.ha-facet').text()).toMatch(/^Power[\d.]+kW[\d.]+ kWh today$/);
    expect(wide.find('.ha-pres .ha-graph').exists()).toBe(true);
    const small = mountHa('small', { options: { power: true } });
    expect(small.find('.v').text()).toMatch(/kW$/);
  });
});
