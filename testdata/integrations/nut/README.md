# NUT fixtures: written transcripts

**Not recorded.** No NUT server was at hand, so these were written from the [NUT network protocol docs](https://networkupstools.org/docs/developer-guide.chunked/net-protocol.html) and the variable names the usbhid-ups and apcsmart drivers use. Record a real upsd (`printf 'VER\nLIST UPS\nLIST VAR <ups>\nLOGOUT\n' | nc <host> 3493`) into them when one is available.

Each file is one session. `> ` lines are what the client sends, `< ` lines upsd's answer, `#` lines comments. The fake upsd in `integrations/nut/nut_test.go` answers each command from its file, handles `USERNAME`, `PASSWORD` and `LOGOUT` itself, and answers anything else with `ERR UNKNOWN-COMMAND`.

| File | What it is |
|---|---|
| `online.txt` | One UPS, `rack` ("Rack UPS", an Eaton 5P 1550), on mains (`OL`), fully charged, 45.5 minutes of runtime |
| `on_battery.txt` | The same UPS four minutes into a power cut: `OB DISCHRG`, 81 %, input 0 V |
| `low_battery.txt` | Later in the cut: `OB DISCHRG LB`, 18 %, 170 s of runtime |
| `several.txt` | Three UPSes: `desk` with no description (`Unavailable`) and no `ups.realpower`, `OL CHRG`; `closet`, whose description has escaped quotes, `OL RB`; `garage`, whose driver's data is stale (`ERR DATA-STALE`) |

Nothing in them is real: the serial is `FAKE00000001`, and the models are stock product names.
