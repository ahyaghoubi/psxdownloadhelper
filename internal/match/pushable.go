package match

// IsPushableKind reports whether a classified URL is worth auto-downloading or
// exporting. We act on the actual package payloads (PKG base/patch/app/sc/delta);
// manifests, CRC sidecars, and unknowns are skipped because they are noise to
// any external downloader.
func IsPushableKind(k Kind) bool {
	switch k {
	case KindPKGBase, KindPKGPatch, KindPKGApp, KindPKGSC, KindPKGDelta:
		return true
	default:
		return false
	}
}
