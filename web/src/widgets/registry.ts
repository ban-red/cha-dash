/**
 * Widget type → component. The catalog (names, sizes, targets) is in
 * model/widgets.ts; the shared widget styles (strip and glyph layouts, rows,
 * tables, disks) ride with the widgets in widgets.css.
 *
 * The widgets the default board uses ride with the front page, as before.
 * The ones a board only has when someone adds them load on demand, each its
 * own small chunk, with a skeleton tile while it arrives; `preloadWidgets()`
 * fetches them ahead of time (the widget picker's previews, tests).
 */
import { defineAsyncComponent, h, type Component } from 'vue';
import './widgets.css';
import ArrWidget from './ArrWidget.vue';
import BackupsWidget from './BackupsWidget.vue';
import CalendarWidget from './CalendarWidget.vue';
import CamerasWidget from './CamerasWidget.vue';
import ClockWidget from './ClockWidget.vue';
import ContainersWidget from './ContainersWidget.vue';
import DownloadsWidget from './DownloadsWidget.vue';
import FleetWidget from './FleetWidget.vue';
import HomeAssistantWidget from './HomeAssistantWidget.vue';
import HostWidget from './HostWidget.vue';
import IncidentsWidget from './IncidentsWidget.vue';
import OpnsenseWidget from './OpnsenseWidget.vue';
import PlexWidget from './PlexWidget.vue';
import ServiceWidget from './ServiceWidget.vue';
import TraefikWidget from './TraefikWidget.vue';
import UptimeKumaWidget from './UptimeKumaWidget.vue';
import WeatherWidget from './WeatherWidget.vue';
import WidgetPending from './parts/WidgetPending.vue';

const failed = { render: () => h(WidgetPending, { failed: true }) };
const lazy = (loader: () => Promise<{ default: Component }>): Component =>
  defineAsyncComponent({ loader, loadingComponent: WidgetPending, errorComponent: failed, delay: 0 });

export const WIDGET_COMPONENTS: Record<string, Component> = {
  arr: ArrWidget,
  backups: BackupsWidget,
  calendar: CalendarWidget,
  cameras: CamerasWidget,
  clock: ClockWidget,
  containers: ContainersWidget,
  downloads: DownloadsWidget,
  fleet: FleetWidget,
  homeassistant: HomeAssistantWidget,
  host: HostWidget,
  incidents: IncidentsWidget,
  opnsense: OpnsenseWidget,
  plex: PlexWidget,
  service: ServiceWidget,
  traefik: TraefikWidget,
  uptimekuma: UptimeKumaWidget,
  weather: WeatherWidget,
  bookmarks: lazy(() => import('./BookmarksWidget.vue')),
  check: lazy(() => import('./CheckWidget.vue')),
  guests: lazy(() => import('./GuestsWidget.vue')),
  metric: lazy(() => import('./MetricWidget.vue')),
  storage: lazy(() => import('./StorageWidget.vue')),
  unraid: lazy(() => import('./UnraidWidget.vue')),
  unraidhost: lazy(() => import('./UnraidHostWidget.vue')),
};

/** Fetch every on-demand widget now; afterwards they render without a skeleton. */
export async function preloadWidgets(): Promise<void> {
  await Promise.all(
    Object.values(WIDGET_COMPONENTS).map((c) => (c as { __asyncLoader?: () => Promise<unknown> }).__asyncLoader?.()),
  );
}
