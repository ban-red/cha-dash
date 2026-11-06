# Wall displays

A display is a board on a wall tablet or a TV, with nobody at it. It shows one board without menus, edit mode or sign-in. It stays calm while nothing's wrong, and it wakes up when something needs you.

## Make a display

1. In **Settings → Displays**, choose **New display**. Give it a name ("Hall tablet") and choose the board it shows. You can also set quiet hours (below).
2. Cha Dash shows the display's link **once**: `https://<your hub>/display/<board>#cdd_…`. Open it on the device. Copy it now: you can't see it again, only make a new one.
3. The device keeps a cookie for the display, and the secret leaves the address bar. The cookie lasts about 13 months, it's sent only to the display's own routes, and Cha Dash renews nothing for it.

Only admins and the owner can make displays. **Remove** in Settings → Displays signs the device out at once. Do it if a tablet goes missing.

## What a display can and can't do

A display reads its one board and what household reads for it:

- the incidents and backup coverage that board shows, as household sees them (no incident notes, no security findings). A board with the **Incidents** widget, or one never saved, shows household's whole Inbox view;
- the Home Graph and metrics as household sees them, which tiles need to draw: names, states and health, without IP or MAC addresses or chadash-node's details.

It can't act, acknowledge, open a resource's sheet, read any other board, read a secret, or call any other part of the API. Its secret is refused everywhere but the display's own routes. A stolen tablet exposes what a household account would see, until you remove it.

## How it behaves

- **Calm by default.** Two minutes after the last change the display dims. From 21:00 to 07:00 (the hub's time) it turns warmer.
- **Wakes on a change.** A new incident at *attention* or *down* brings it to full brightness, with the incident's one line on top. It dims again once that's resolved or someone acknowledges it.
- **Acknowledged incidents** show dimmed, with who's on them, and don't wake it.
- **Quiet hours.** If set, the display stays dark between the two times, and only a *down* incident that nobody has acknowledged wakes it.
- **Burn-in care.** The board moves a few pixels every few minutes.
- **Stale is visible.** If the display loses the hub, it says how old its data is instead of looking calm.

## Setting up the device

Any browser works. Turn off the device's own sleep, or let it dim but never lock. Here's one way for each kind of device.

### An Android tablet with Fully Kiosk Browser

1. Install Fully Kiosk Browser and set its **Start URL** to the display's link.
2. Turn on **Keep Screen On**, and leave any screen-off timer off: the display's quiet hours darken it at night instead.
3. Turn on kiosk mode, so nobody can leave the page.
4. Leave cookies on (they are by default). The display needs its cookie.

The link is opened once. After that, the start URL can be the link without its `#…` part.

### A Raspberry Pi with Chromium in kiosk mode

On Raspberry Pi OS with the desktop, add Chromium to the session's autostart. With labwc (the default since Bookworm), add this line to `~/.config/labwc/autostart`:

```sh
chromium-browser --kiosk --noerrdialogs --disable-session-crashed-bubble 'https://dash.example.home/display/brd_…#cdd_…'
```

Use your own link. Chromium keeps the cookie in its profile, so later starts can drop the `#…` part. To stop the screen blanking, turn off **Screen Blanking** in Raspberry Pi Configuration → Display.

### An iPad or iPhone in Guided Access

1. Open the link in Safari, then **Share → Add to Home Screen**, and open the display from there.
2. In **Settings → Accessibility → Guided Access**, turn it on. Set **Display Auto-Lock** to **Never**.
3. Open the display and triple-click the side (or Home) button to start Guided Access.

Plug the device in. A display draws little, but it never sleeps.

## If a display says it isn't signed in

The display was removed, or its cookie was cleared. Make a new display for it in Settings → Displays and open the new link. Its old link no longer works.
