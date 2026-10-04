/** Widget type → component. The catalog (names, sizes) is in model/widgets.ts. */
import type { Component } from 'vue';
import ArrWidget from './ArrWidget.vue';
import BackupsWidget from './BackupsWidget.vue';
import CalendarWidget from './CalendarWidget.vue';
import CamerasWidget from './CamerasWidget.vue';
import ContainersWidget from './ContainersWidget.vue';
import DownloadsWidget from './DownloadsWidget.vue';
import FleetWidget from './FleetWidget.vue';
import HomeAssistantWidget from './HomeAssistantWidget.vue';
import HostWidget from './HostWidget.vue';
import IncidentsWidget from './IncidentsWidget.vue';
import OpnsenseWidget from './OpnsenseWidget.vue';
import PlexWidget from './PlexWidget.vue';
import TraefikWidget from './TraefikWidget.vue';
import UptimeKumaWidget from './UptimeKumaWidget.vue';

export const WIDGET_COMPONENTS: Record<string, Component> = {
  arr: ArrWidget,
  backups: BackupsWidget,
  calendar: CalendarWidget,
  cameras: CamerasWidget,
  containers: ContainersWidget,
  downloads: DownloadsWidget,
  fleet: FleetWidget,
  homeassistant: HomeAssistantWidget,
  host: HostWidget,
  incidents: IncidentsWidget,
  opnsense: OpnsenseWidget,
  plex: PlexWidget,
  traefik: TraefikWidget,
  uptimekuma: UptimeKumaWidget,
};
