"""The device every Cha Dash entity belongs to."""

from __future__ import annotations

from homeassistant.helpers.device_registry import DeviceEntryType, DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import CONF_URL, DOMAIN
from .coordinator import ChaDashCoordinator


class ChaDashEntity(CoordinatorEntity[ChaDashCoordinator]):
    _attr_has_entity_name = True

    def __init__(self, coordinator: ChaDashCoordinator, key: str) -> None:
        super().__init__(coordinator)
        entry = coordinator.entry
        self._attr_unique_id = f"{entry.entry_id}_{key}"
        self._attr_device_info = DeviceInfo(
            identifiers={(DOMAIN, entry.entry_id)},
            name="Cha Dash",
            manufacturer="Cha Dash",
            entry_type=DeviceEntryType.SERVICE,
            configuration_url=entry.data[CONF_URL],
        )
