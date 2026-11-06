"""On while something in the home needs a person: the one to automate on."""

from __future__ import annotations

from homeassistant.components.binary_sensor import BinarySensorDeviceClass, BinarySensorEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers.entity_platform import AddEntitiesCallback

from .coordinator import ChaDashCoordinator
from .entity import ChaDashEntity


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry, add: AddEntitiesCallback) -> None:
    add([ProblemSensor(entry.runtime_data)])


class ProblemSensor(ChaDashEntity, BinarySensorEntity):
    _attr_name = "Problem"
    _attr_device_class = BinarySensorDeviceClass.PROBLEM

    def __init__(self, c: ChaDashCoordinator) -> None:
        super().__init__(c, "problem")

    @property
    def is_on(self) -> bool:
        return self.coordinator.data.problem

    @property
    def extra_state_attributes(self) -> dict:
        return {"headline": self.coordinator.data.headline}
