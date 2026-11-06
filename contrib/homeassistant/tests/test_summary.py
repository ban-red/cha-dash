"""The summary parser, with the standard library only: python3 -m unittest discover tests."""

import importlib.util
import pathlib
import sys
import unittest

spec = importlib.util.spec_from_file_location("summary", pathlib.Path(__file__).parent.parent / "custom_components" / "chadash" / "summary.py")
summary = importlib.util.module_from_spec(spec)
sys.modules["summary"] = summary  # dataclasses look the module up
spec.loader.exec_module(summary)

BODY = {
    "now": "2026-11-06T10:00:00Z",
    "health": "down",
    "incidents": {"open": 2, "down": 1, "attention": 1, "acknowledged": 0},
    "top": [
        {"id": "inc_1", "title": "The NAS is down", "severity": "down", "target": "host:nas"},
        {"id": "inc_2", "title": "Backups are late", "severity": "attention", "acknowledged_by": "sam"},
    ],
    "updates": {"available": 3},
}


class ParseTest(unittest.TestCase):
    def test_reads_a_summary(self):
        s = summary.parse(BODY)
        self.assertEqual(s.health, "down")
        self.assertTrue(s.problem)
        self.assertEqual(s.headline, "The NAS is down")
        self.assertEqual((s.open, s.down, s.attention, s.acknowledged), (2, 1, 1, 0))
        self.assertEqual(s.top[1].acknowledged_by, "sam")
        self.assertEqual(s.updates_available, 3)

    def test_household_has_no_updates(self):
        s = summary.parse({**BODY, "updates": None})
        self.assertIsNone(s.updates_available)

    def test_all_quiet(self):
        s = summary.parse({"now": "", "health": "ok", "incidents": {"open": 0, "down": 0, "attention": 0, "acknowledged": 0}, "top": [], "updates": None})
        self.assertFalse(s.problem)
        self.assertEqual(s.headline, "")

    def test_refuses_what_isnt_a_summary(self):
        for bad in [None, [], {"health": "fine"}, {**BODY, "incidents": None}, {**BODY, "incidents": {**BODY["incidents"], "open": -1}}, {**BODY, "top": [{"title": "no id"}]}]:
            with self.subTest(bad=bad), self.assertRaises(summary.SummaryError):
                summary.parse(bad)


if __name__ == "__main__":
    unittest.main()
