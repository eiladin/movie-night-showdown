import type { SourceID } from './api'

// Accent colors for source chips and badges.
//
// TMDB's watch-provider data carries no brand color — only an id, a name, and a
// logo — so there is nothing to read one from. With an open source set, a
// hand-written table alone would mean most providers render colorless while a
// favored few are branded, which reads as broken rather than minimal.
//
// So: known brands keep their real colors, and everything else is assigned a
// stable color from a fixed palette. Every provider gets one, no provider needs
// a code change, and a given service always looks the same.

// BRAND_ACCENTS are the real colors of services common enough to be recognized
// by them. Values are chosen to stay legible both on the light setup page and
// on the dark result screen, so a single value serves chip and badge alike.
const BRAND_ACCENTS: Record<string, string> = {
    jellyfin: '#00a4dc',
    netflix: '#e50914',
    prime: '#00a8e1',
    disney: '#4f7cff',
    hulu: '#1ce783',
    peacock: '#f0b429',
    max: '#8a5cf6',
    apple: '#9aa0a6',
    paramount: '#3d7bff',
}

// FALLBACK_PALETTE covers every other provider. The hues are spread around the
// wheel and held to a middle lightness so each one is distinguishable from its
// neighbours and readable against both backgrounds.
const FALLBACK_PALETTE = [
    '#e0567a',
    '#e08a2a',
    '#c2a227',
    '#5aa85e',
    '#3fa9a0',
    '#4a8fe0',
    '#7a6fe0',
    '#b05fc4',
    '#cc6a55',
    '#5f93a8',
]

// sourceAccent returns the color for one source. The fallback is a hash rather
// than a counter so a service's color never depends on how many others happen
// to be configured, or on the order they were listed in.
export function sourceAccent(id: SourceID): string {
    const brand = BRAND_ACCENTS[id]
    if (brand) return brand

    let hash = 0
    for (let i = 0; i < id.length; i++) {
        // Standard string hash; `| 0` keeps it in 32-bit range.
        hash = (hash * 31 + id.charCodeAt(i)) | 0
    }
    return FALLBACK_PALETTE[Math.abs(hash) % FALLBACK_PALETTE.length]
}

// accentStyle exposes the accent to CSS as a custom property, so the styling
// itself stays in the stylesheets rather than becoming inline color rules.
export function accentStyle(id: SourceID): React.CSSProperties {
    return { ['--source-accent' as string]: sourceAccent(id) }
}
