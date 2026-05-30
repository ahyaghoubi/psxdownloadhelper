# psxdownloadhelper

A cross-platform Go HTTP proxy for PlayStation owners. Sits between a PS5/PS4 and Sony's CDN, captures the official download URLs, hands them to Free Download Manager (FDM) on a PC, watches a library folder, and serves the downloaded `.pkg` files back to the console over LAN with full HTTP `Range` support.

## Status

Pre-implementation. Phase 0 (hardware validation) in progress. See [plan.md](plan.md) for the full product and technical specification and [plan.md §8](plan.md#8-implementation-phases) for the phase breakdown. Architecture decision records live in [docs/decisions/](docs/decisions/).

## Licence

MIT. See [LICENSE](LICENSE).
# psxdownloadhelper
