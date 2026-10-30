/**
 * The Home Assistant tile's house power setting (widgets/HomeAssistantWidget.vue,
 * `options.power`): off by default, with the house's checks in its place.
 */
import { afterEach, describe, expect, it } from 'vitest';
import { computed, defineComponent, h as el } from 'vue';
import type { SizeId } from '@chadash/layout';
import HomeAssistantWidget from '../src/widgets/HomeAssistantWidget.vue';
import { WIDGETS } from '../src/model/widgets';
import { optionValue } from '../src/widgets/options';
import { provideTile, type TileConfig } from '../src/stores/tiles';
import { docs, healthy } from './fixtures';
import { harness } from './harness';

afterEach(() => {
  document.body.innerHTML = '';
});

/** The Demo Home, with what the integration reads from a fuller house. */
function fullerHouse(tripped = false) {
  const d = docs(healthy);
  const base = d.metrics.series['app:homeassistant#people_home']!;
  const put = (k: string, value: number) => (d.metrics.series[`app:homeassistant#${k}`] = { ...base, value });
  put('safety_tripped', tripped ? 1 : 0);
  put('media_playing', 1);
  const app = d.graph.resources['app:homeassistant']!;
  app.attrs = {
    ...app.attrs,
    temperature_unit: '°C',
    people_items: [
      { name: 'Sam', where: 'home' },
      { name: 'Alex', where: 'away' },
      { name: 'Jo', where: 'home' },
    ],
    climate_items: [
      { name: 'Living room', current: 21.5, target: 22, action: 'heating' },
      { name: 'Bedroom', current: 18.9, target: 17, action: 'idle' },
    ],
    safety_total: 3,
    safety_items: tripped ? [{ name: 'Utility room leak', kind: 'moisture' }] : [],
    media_total: 3,
    media_items: [{ name: 'Living room speaker', title: 'Weightless', artist: 'Marconi Union' }],
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

describe('Home Assistant house power', () => {
  it('is off unless turned on', () => {
    const [power] = WIDGETS['homeassistant']!.options!;
    expect(optionValue(power!, undefined)).toBe(false);
    expect(optionValue(power!, true)).toBe(true);
  });

  it('shows no power by default, and the checks when the house has nothing else to show', () => {
    for (const size of ['strip', 'small', 'wide', 'large'] as const) {
      const w = mountHa(size);
      expect(w.text(), size).not.toMatch(/kW|kWh/);
      w.unmount();
    }
    // A house with no thermostat, safety sensor or media player has the checks.
    const bare = docs(healthy);
    const app = bare.graph.resources['app:homeassistant']!;
    app.attrs = { ...app.attrs, climate_items: [], safety_total: 0, media_total: 0 };
    const wide = mountHa('wide', undefined, bare);
    expect(wide.findAll('.ha-check').map((c) => c.find('dt').text())).toEqual(['open', 'unlocked', 'low batteries', 'updates']);
    expect(wide.find('.col').exists()).toBe(false);
  });

  it('shows power, its graph and energy today when turned on', () => {
    const wide = mountHa('wide', { options: { power: true } });
    expect(wide.find('.col').text()).toMatch(/kW/);
    expect(wide.text()).toContain('kWh today');
    expect(wide.find('.ha-checks').exists()).toBe(false);
    const small = mountHa('small', { options: { power: true } });
    expect(small.find('.v').text()).toMatch(/kW$/);
  });

  it('shows indoors, safety and playing cards, and names who is home', () => {
    const w = mountHa('wide', undefined, fullerHouse());
    const cards = w.findAll('.ha-card').map((c) => c.text());
    expect(cards).toEqual(['20.2°Cindoors2 rooms · heating', 'OKsafety3 sensors, all quiet', '1playingWeightless · Marconi Union']);
    expect(w.find('.ha-checks').exists()).toBe(false);
    expect(w.find('.ha-names').text()).toBe('Sam, Jo');
    expect(w.find('.is-attention, [data-status="attention"]').exists()).toBe(false);
  });

  it('marks the tile and heads the list when a safety sensor trips', () => {
    const w = mountHa('wide', undefined, fullerHouse(true));
    const safety = w.findAll('.ha-card').find((c) => c.text().includes('tripped'))!;
    expect(safety.classes()).toContain('flag');
    expect(safety.text()).toContain('Utility room leak · water leak');
    expect(w.text()).toContain('Safety alert');
  });

  it('leaves out a card the house has no sensors for', () => {
    const d = fullerHouse();
    d.graph.resources['app:homeassistant']!.attrs = { ...d.graph.resources['app:homeassistant']!.attrs, climate_items: [], media_total: 0 };
    const w = mountHa('wide', undefined, d);
    expect(w.findAll('.ha-card').map((c) => c.find('.lab').text())).toEqual(['safety']);
  });
});
