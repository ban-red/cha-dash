/**
 * The Home Assistant tile (widgets/HomeAssistantWidget.vue): presence, the
 * facets, the People, Climate and Playing views, and the house power
 * setting (`options.power`), off by default. Its panel
 * (widgets/HomeAssistantPanel.vue): the lists behind the facets, and the
 * controls the instance's `control` allows.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { computed, defineComponent, h as el } from 'vue';
import type { SizeId } from '@chadash/layout';
import { flushPromises } from '@vue/test-utils';
import HomeAssistantWidget from '../src/widgets/HomeAssistantWidget.vue';
import HomeAssistantPanel from '../src/widgets/HomeAssistantPanel.vue';
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

/** The fuller house with every light, switch and player, and the controls allowed. */
function controlled(control: string[] = ['light', 'switch', 'media_player', 'climate']) {
  const d = fullerHouse();
  const app = d.graph.resources['app:homeassistant']!;
  app.attrs = {
    ...app.attrs,
    control,
    light_items: [
      { entity: 'light.kitchen', name: 'Kitchen', state: 'on', brightness: 70 },
      { entity: 'light.hallway', name: 'Hallway', state: 'off' },
      { entity: 'light.porch', name: 'Porch', state: 'unavailable' },
    ],
    switch_items: [{ entity: 'switch.server_rack', name: 'Server rack', state: 'on' }],
    player_items: [
      { entity: 'media_player.living_room_speaker', name: 'Living room speaker', state: 'playing', title: 'Weightless', artist: 'Marconi Union' },
      { entity: 'media_player.bedroom_tv', name: 'Bedroom TV', state: 'paused' },
      { entity: 'media_player.kitchen_display', name: 'Kitchen display', state: 'off' },
    ],
    climate_items: [
      { entity: 'climate.living_room', name: 'Living room', current: 21.5, target: 22, action: 'heating', mode: 'heat', min: 7, max: 22.5, step: 0.5 },
      { entity: 'climate.bedroom', name: 'Bedroom', current: 18.9, target: 17, action: 'off', mode: 'off' },
    ],
  };
  return d;
}

function mountPanel(data: ReturnType<typeof docs>, tab: string, role = 'owner') {
  const hs = harness(data);
  hs.session.state.user = { id: 'u1', username: role, role };
  const run = vi.fn(async () => {});
  hs.ui.runAction = run;
  hs.ui.openHomePanel('app:homeassistant', tab);
  const w = hs.mountIn(HomeAssistantPanel);
  return { hs, w, run };
}

function mountHa(size: SizeId, config?: TileConfig, data = docs(healthy)) {
  const hs = harness(data);
  const Host = defineComponent({
    setup() {
      provideTile({ itemId: 'homeassistant@app:homeassistant', type: 'homeassistant', target: 'app:homeassistant', config: computed(() => config) });
      return () => el('main', [el(HomeAssistantWidget, { size, target: 'app:homeassistant' })]);
    },
  });
  return Object.assign(hs.mountIn(Host), { hs });
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

describe('Home Assistant click-through', () => {
  it('opens the panel on the list behind a facet, the presence line and the needs-a-look line', async () => {
    const d = controlled();
    d.graph.resources['app:homeassistant']!.attrs!['battery_items'] = [{ name: 'Hallway motion', level: 12 }];
    d.metrics.series['app:homeassistant#batteries_low'] = { ...d.metrics.series['app:homeassistant#people_home']!, value: 1 };
    const w = mountHa('wide', undefined, d);
    const open = w.findAll('.ha-facet .ha-open');
    // Safety's OK and the doors are shut: nothing behind them to show.
    expect(open.map((b) => b.attributes('aria-label'))).toEqual([
      'Indoors: 20.2°C, Living room heating. Show all',
      expect.stringMatching(/^Lights: \d+\/52, \d+ switches\. Show all$/),
      'Playing: 1, Weightless · Marconi Union. Show all',
    ]);
    await open[1]!.trigger('click');
    expect(w.hs.ui.state.homePanel).toEqual({ app: 'app:homeassistant', tab: 'lights' });
    await w.find('button.ha-who').trigger('click');
    expect(w.hs.ui.state.homePanel?.tab).toBe('people');
    await w.find('.ha-look').trigger('click');
    expect(w.hs.ui.state.homePanel?.tab).toBe('look');
  });

  it('opens Needs a look from a flagged Doors facet', async () => {
    const d = controlled();
    d.graph.resources['app:homeassistant']!.attrs!['open_items'] = [{ name: 'Back door', kind: 'door' }];
    d.metrics.series['app:homeassistant#open'] = { ...d.metrics.series['app:homeassistant#people_home']!, value: 1 };
    const w = mountHa('wide', undefined, d);
    const doors = w.find('.ha-doors .ha-open');
    expect(doors.attributes('aria-label')).toBe('Doors: 1, Back door open. Show all');
    await doors.trigger('click');
    expect(w.hs.ui.state.homePanel?.tab).toBe('look');
  });

  it('opens nothing while the board is being edited', async () => {
    const w = mountHa('wide', undefined, controlled());
    w.hs.ui.state.editing = true;
    await flushPromises();
    expect(w.find('.ha-open').attributes('tabindex')).toBe('-1');
    await w.find('.ha-open').trigger('click');
    expect(w.hs.ui.state.homePanel).toBeUndefined();
  });

  it('leaves a facet with nothing behind it as it was', () => {
    const w = mountHa('wide', undefined, fullerHouse());
    // The Demo Home sends no light, switch or player lists.
    expect(w.findAll('.ha-facet.opens dt').map((x) => x.text())).toEqual(['Indoors']);
  });
});

describe('Home Assistant panel', () => {
  it('lists every light, on first, with a switch each, and turns one on with Undo', async () => {
    const { w, run } = mountPanel(controlled(), 'lights');
    await flushPromises();
    expect(w.findAll('[role="tab"]').map((t) => [t.text(), t.attributes('aria-selected')])).toEqual([
      ['People', 'false'],
      ['Lights', 'true'],
      ['Switches', 'false'],
      ['Climate', 'false'],
      ['Playing', 'false'],
    ]);
    expect(document.activeElement).toBe(w.find('[role="tab"][aria-selected="true"]').element);
    expect(w.find('.ha-sum').text()).toBe('1 of 3 on');
    expect(w.findAll('.ha-row .ha-main').map((r) => r.text())).toEqual(['Kitchen70%', 'Hallway', 'Porch']);
    const sw = w.findAll('button[role="switch"]');
    expect(sw.map((b) => [b.attributes('aria-label'), b.attributes('aria-checked')])).toEqual([
      ['Kitchen', 'true'],
      ['Hallway', 'false'],
    ]);
    expect(w.findAll('.ha-row')[2]!.find('.ha-side').text()).toBe('Unavailable');

    await sw[1]!.trigger('click');
    expect(run).toHaveBeenCalledWith({ action: 'homeassistant.entity.on', target: 'app:homeassistant', params: { entity: 'light.hallway' }, label: 'Turn on Hallway', tier: 'T1' });
    // Shown as asked until the next read agrees.
    expect(w.findAll('button[role="switch"]')[1]!.attributes('aria-checked')).toBe('true');
    expect(await axeViolations(w.element)).toEqual([]);
  });

  it('moves between tabs with the arrow keys', async () => {
    const { w } = mountPanel(controlled(), 'lights');
    await flushPromises();
    await w.find('[role="tab"][aria-selected="true"]').trigger('keydown', { key: 'ArrowRight' });
    await flushPromises();
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe('Switches');
    expect(w.find('.ha-main').text()).toBe('Server rack');
    await w.find('[role="tab"][aria-selected="true"]').trigger('keydown', { key: 'Home' });
    expect(w.find('[role="tab"][aria-selected="true"]').text()).toBe('People');
  });

  it('plays and pauses players, and sets a thermostat once the clicks settle', async () => {
    vi.useFakeTimers();
    const { w, run, hs } = mountPanel(controlled(), 'media');
    await flushPromises();
    const play = w.findAll('.ha-play');
    expect(play.map((b) => b.attributes('aria-label'))).toEqual(['Pause Living room speaker', 'Play Bedroom TV']);
    await play[1]!.trigger('click');
    expect(run).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'homeassistant.media.play', params: { entity: 'media_player.bedroom_tv' } }));

    hs.ui.openHomePanel('app:homeassistant', 'climate');
    await flushPromises();
    // The bedroom's is off: no stepper.
    expect(w.findAll('.ha-step').length).toBe(1);
    const [lower, raise] = w.findAll('.ha-step button');
    await raise!.trigger('click');
    expect(w.find('.ha-step output').text()).toBe('22.5°C');
    // Its max: no higher.
    expect(w.find('.ha-step button:last-child').attributes('disabled')).toBeDefined();
    await lower!.trigger('click');
    await lower!.trigger('click');
    expect(run).toHaveBeenCalledTimes(1);
    vi.advanceTimersByTime(800);
    expect(run).toHaveBeenLastCalledWith({ action: 'homeassistant.climate.set', target: 'app:homeassistant', params: { entity: 'climate.living_room', temperature: 21.5 }, label: 'Set Living room to 21.5°', tier: 'T1' });
    expect(run).toHaveBeenCalledTimes(2);
  });

  it('offers no controls the instance doesn’t allow, and says where to allow them', async () => {
    const { w } = mountPanel(controlled(['media_player']), 'lights');
    await flushPromises();
    expect(w.find('button[role="switch"]').exists()).toBe(false);
    expect(w.findAll('.ha-row .ha-side').map((x) => x.text())).toEqual(['On', 'Off', 'Unavailable']);
    expect(w.find('.ha-hint').text()).toContain('To switch lights from here, allow them');
  });

  it('offers a viewer no controls, and household lights but not switches', async () => {
    const viewer = mountPanel(controlled(), 'lights', 'viewer');
    await flushPromises();
    expect(viewer.w.find('button[role="switch"]').exists()).toBe(false);
    expect(viewer.w.find('.ha-hint').exists()).toBe(false);
    viewer.w.unmount();

    const house = mountPanel(controlled(['light', 'media_player']), 'lights', 'household');
    await flushPromises();
    expect(house.w.findAll('button[role="switch"]').length).toBe(2);
    house.w.unmount();
    // With switches allowed, the hub doesn't let household switch lights either.
    const both = mountPanel(controlled(['light', 'switch']), 'lights', 'household');
    await flushPromises();
    expect(both.w.find('button[role="switch"]').exists()).toBe(false);
  });

  it('closes with its button', async () => {
    const { w, hs } = mountPanel(controlled(), 'people');
    await flushPromises();
    expect(w.findAll('.ha-row .ha-main').map((r) => r.text().replace(/since.*/, ''))).toEqual(['Jo', 'Sam', 'Robin']);
    await w.find('.act-dlg-head .x').trigger('click');
    expect(hs.ui.state.homePanel).toBeUndefined();
  });
});
