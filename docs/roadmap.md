# Roadmap

## Vision

Help PlayStation owners download content they own faster: capture the
official CDN URLs the console asks for, let the user download them on a PC
with the downloader they already use (FDM, aria2, IDM, …), and serve the
files back to the console over the LAN.

psxdh deliberately does one thing. It is not a downloader.

## Non-goals

- A built-in downloader, download cluster, or download queue. FDM and
  friends already do this well. (ADR 0005 added one; ADR 0006 removed it.)
- Custom DNS resolvers, retry and circuit-breaker stacks, bandwidth shaping.
- Piracy, licence bypass, or content the user doesn't own.
- HTTPS interception of any PSN traffic.
- Homebrew / Remote Package Installer sideloading.

## Next: validate on real hardware

This has never been done and matters more than any new feature. Until it is,
"works" means "works against our simulation of the CDN".

- [ ] PS5: one small title, one large multi-part title, one title update.
      Record which URLs arrive as plain HTTP and which are CONNECT-only.
- [ ] PS5: pause on console → download in FDM → resume. Confirm the
      install bar continues and the install completes.
- [ ] PS5: confirm the order and naming of `_N.pkg` parts, and whether
      `_sc.pkg`, `-DP.pkg`, `.crc` and `.json` are proxy-visible.
- [ ] PS4: same flow for one `appkgo` multi-part title.
- [ ] FDM: confirm a pasted URL (with its query string) downloads the same
      bytes and keeps the original file name.
- [ ] Save redacted real URLs as test fixtures for `internal/match`.

If PS5 game packages turn out to be CONNECT-only on current firmware, the
proxy approach can't replace them and the README must say so.

## Ideas, only after hardware validation

- **Game names** instead of only title IDs.
- **Windows tray icon** so the console window can be hidden.

## Risks

| Risk | Mitigation |
| --- | --- |
| Sony changes URL layout | Rules are small YAML files in `internal/match/rules` |
| Wrong file served | Exact name match, size check against Sony, never serve temp names |
| Proxy breaks PSN sign-in or Store | HTTPS is tunnelled untouched; only plain-HTTP CDN files are ever replaced |
| Large files | Everything streams; memory stays flat |
