"""Cha Dash's health and counts as sensors."""

from __future__ import annotations

from homeassistant.components.sensor import SensorDeviceClass, SensorEntity, SensorStateClass
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .coordinator import ChaDashCoordinator
from .entity import ChaDashEntity
from .summary import HEALTHS


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry, add: AddEntitiesCallback) -> None:
    c: ChaDashCoordinator = entry.runtime_data
    entities: list[SensorEntity] = [
        HealthSensor(c),
        CountSensor(c, "open_problems", "Open problems", lambda s: s.open),
        CountSensor(c, "down", "Down", lambda s: s.down),
        CountSensor(c, "acknowledged", "Acknowledged", lambda s: s.acknowledged),
    ]
    if c.data.updates_available is not None:
        entities.append(CountSensor(c, "updates", "Updates waiting", lambda s: s.updates_available))
    add(entities)


class HealthSensor(ChaDashEntity, SensorEntity):
    """ok, attention or down, with the worst open problems as attributes."""

    _attr_translation_key = "health"
    _attr_name = "Health"
    _attr_device_class = SensorDeviceClass.ENUM
    _attr_options = list(HEALTHS)

    def __init__(self, c: ChaDashCoordinator) -> None:
        super().__init__(c, "health")

    @property
    def native_value(self) -> str:
        return self.coordinator.data.health

    @property
    def extra_state_attributes(self) -> dict:
        s = self.coordinator.data
        return {
            "headline": s.headline,
            "top": [{"title": i.title, "severity": i.severity, "target": i.target, "acknowledged_by": i.acknowledged_by} for i in s.top],
        }


class CountSensor(ChaDashEntity, SensorEntity):
    _attr_state_class = SensorStateClass.MEASUREMENT

    def __init__(self, c: ChaDashCoordinator, key: str, name: str, value) -> None:
        super().__init__(c, key)
        self._attr_name = name
        self._value = value

    @property
    def native_value(self) -> int | None:
        return self._value(self.coordinator.data)
