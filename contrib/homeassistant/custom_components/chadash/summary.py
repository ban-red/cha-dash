"""Reading Cha Dash's GET /api/summary.

Plain Python with no Home Assistant imports, so it's tested on its own
(tests/test_summary.py). The hub's answer is documented in Cha Dash's
internal/README.md and its OpenAPI document.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

HEALTHS = ("ok", "attention", "down")


class SummaryError(ValueError):
    """The hub's answer isn't a summary."""


@dataclass(frozen=True)
class Incident:
    id: str
    title: str
    severity: str
    target: str = ""
    opened_at: str = ""
    acknowledged_by: str = ""


@dataclass(frozen=True)
class Summary:
    now: str
    health: str
    open: int
    down: int
    attention: int
    acknowledged: int
    top: list[Incident] = field(default_factory=list)
    # None when the token's user doesn't see updates (household).
    updates_available: int | None = None

    @property
    def problem(self) -> bool:
        """Something needs a person: the home isn't ok."""
        return self.health != "ok"

    @property
    def headline(self) -> str:
        """The worst open problem's title, or "" when there's none."""
        return self.top[0].title if self.top else ""


def _int(v: Any, name: str) -> int:
    if isinstance(v, bool) or not isinstance(v, int) or v < 0:
        raise SummaryError(f"{name} isn't a count")
    return v


def parse(body: Any) -> Summary:
    """Turns the decoded JSON of GET /api/summary into a Summary."""
    if not isinstance(body, dict):
        raise SummaryError("the answer isn't an object")
    health = body.get("health")
    if health not in HEALTHS:
        raise SummaryError("health isn't ok, attention or down")
    inc = body.get("incidents")
    if not isinstance(inc, dict):
        raise SummaryError("incidents is missing")
    top = []
    for raw in body.get("top") or []:
        if not isinstance(raw, dict) or not isinstance(raw.get("id"), str):
            raise SummaryError("a top incident has no id")
        top.append(
            Incident(
                id=raw["id"],
                title=str(raw.get("title", "")),
                severity=str(raw.get("severity", "")),
                target=str(raw.get("target", "")),
                opened_at=str(raw.get("opened_at", "")),
                acknowledged_by=str(raw.get("acknowledged_by", "")),
            )
        )
    updates = body.get("updates")
    available = None
    if isinstance(updates, dict):
        available = _int(updates.get("available"), "updates.available")
    return Summary(
        now=str(body.get("now", "")),
        health=health,
        open=_int(inc.get("open"), "incidents.open"),
        down=_int(inc.get("down"), "incidents.down"),
        attention=_int(inc.get("attention"), "incidents.attention"),
        acknowledged=_int(inc.get("acknowledged"), "incidents.acknowledged"),
        top=top,
        updates_available=available,
    )
