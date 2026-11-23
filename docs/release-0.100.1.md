# Core 0.100.1: browser hardening headers

Every answer gets two more headers, set in `ServeHTTP` (`internal/service/http.go`) next to `X-Content-Type-Options`:

- `Permissions-Policy`: camera, microphone, location, USB, serial, HID, Bluetooth, MIDI, motion sensors and screen capture off. No page uses them. A form's photo field is a file input, which this doesn't affect.
- `Cross-Origin-Opener-Policy: same-origin-allow-popups`: another site's window can't keep a handle on ours. Popups we open (Paddle's checkout, for example) still work.

No schema, grant, configuration, dependency or tool schema change. `TestWebReadableSpaceAgainstDatabase` checks both headers.

Check after install, on the server:

```bash
curl -sk -D - -o /dev/null https://localhost/ | grep -i -E 'permissions-policy|cross-origin-opener-policy'
```
