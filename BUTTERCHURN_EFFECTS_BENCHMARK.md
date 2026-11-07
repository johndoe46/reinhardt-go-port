# Butterchurn effects CPU benchmark

Audit date: 2026-11-07

This audit ranks the 30 Butterchurn presets bundled with Reinhardt by CPU load.
"Effect" refers to a user-selectable Butterchurn preset, not an individual
shader pass.

## Scope and method

- Preset source: `internal/assets/static/vendor/butterchurn/presets-2.4.7.json`.
- Render path: `internal/assets/static/butterchurn-visualization.js`.
- Resolution: **1280×720**, the maximum offscreen render size used by the app.
- Browser: headless Chrome 156.0.8078.80.
- Hardware: Apple M1, ANGLE Metal renderer.
- Workload: 60 frames per sample, 8 rounds, median after discarding the
  warm-up round.
- **CPU load**: Chrome process-tree CPU time per rendered frame, in milliseconds.
  This includes preset expression evaluation, custom wave/shape vertex work,
  WebGL driver work, and the canvas copy. At 60 fps:

  `% of one CPU core = ms/frame × 6`

- **CPU-only**: the same workload forced through SwiftShader, which moves
  rasterization and shader work onto the CPU. Use this as the worst-case CPU
  ranking.
- **Render call**: main-thread `visualizer.render()` time on the M1.

No application code or assets were changed for this audit. The benchmark
harness was run outside the repository.

## Ranking by CPU load

| # | Effect | CPU load (M1) | CPU-only | Render call |
|---:|---|---:|---:|---:|
| 1 | `martin - witchcraft reloaded` | 4.62 ms/frame | 8.24 ms/frame | 7.10 ms/frame |
| 2 | `Martin - QBikal - Surface Turbulence IIb` | 4.02 ms/frame | 9.00 ms/frame | 2.30 ms/frame |
| 3 | `martin - extreme heat` | 2.79 ms/frame | 7.17 ms/frame | 7.10 ms/frame |
| 4 | `Flexi - alien fish pond` | 2.29 ms/frame | 6.80 ms/frame | 1.50 ms/frame |
| 5 | `flexi - bouncing balls [double mindblob neon mix]` | 1.93 ms/frame | 5.83 ms/frame | 1.50 ms/frame |
| 6 | `_Mig_085` | 1.87 ms/frame | 5.45 ms/frame | 1.20 ms/frame |
| 7 | `suksma - uninitialized variabowl (hydroponic chronic)` | 1.71 ms/frame | 5.54 ms/frame | 3.80 ms/frame |
| 8 | `Flexi, fishbrain, Geiss + Martin - tokamak witchery` | 1.36 ms/frame | 5.12 ms/frame | 1.00 ms/frame |
| 9 | `Eo.S. + Phat - cubetrace - v2` | 1.36 ms/frame | 4.50 ms/frame | 1.00 ms/frame |
| 10 | `martin - chain breaker` | 1.27 ms/frame | 4.57 ms/frame | 1.10 ms/frame |
| 11 | `$$$ Royal - Mashup (197)` | 1.26 ms/frame | 4.88 ms/frame | 1.10 ms/frame |
| 12 | `Flexi - mindblob [shiny mix]` | 1.00 ms/frame | 4.54 ms/frame | 0.80 ms/frame |
| 13 | `An AdamFX n Martin Infusion 2 flexi - Why The Sky Looks Diffrent Today - AdamFx n Martin Infusion - Tack Tile Disfunction B` | 0.96 ms/frame | 4.53 ms/frame | 1.90 ms/frame |
| 14 | `Flexi, martin + geiss - dedicated to the sherwin maxawow` | 0.82 ms/frame | 4.29 ms/frame | 0.60 ms/frame |
| 15 | `martin [shadow harlequins shape code] - fata morgana` | 0.72 ms/frame | 4.44 ms/frame | 0.70 ms/frame |
| 16 | `Geiss - Thumb Drum` | 0.68 ms/frame | 4.25 ms/frame | 0.50 ms/frame |
| 17 | `yin - 191 - Temporal singularities` | 0.67 ms/frame | 3.99 ms/frame | 0.50 ms/frame |
| 18 | `martin - reflections on black tiles` | 0.65 ms/frame | 4.19 ms/frame | 0.50 ms/frame |
| 19 | `Idiot - Star Of Annon` | 0.62 ms/frame | 3.95 ms/frame | 0.50 ms/frame |
| 20 | `Milk Artist At our Best - FED - SlowFast Ft AdamFX n Martin - HD CosmoFX` | 0.61 ms/frame | 3.90 ms/frame | 0.50 ms/frame |
| 21 | `Unchained - Rewop` | 0.50 ms/frame | 3.81 ms/frame | 0.40 ms/frame |
| 22 | `Unchained & Rovastar - Wormhole Pillars (Hall of Shadows mix)` | 0.50 ms/frame | 3.97 ms/frame | 0.40 ms/frame |
| 23 | `Zylot - Paint Spill (Music Reactive Paint Mix)` | 0.42 ms/frame | 4.02 ms/frame | 0.40 ms/frame |
| 24 | `Martin - acid wiring` | 0.41 ms/frame | 4.13 ms/frame | 0.30 ms/frame |
| 25 | `Geiss - Cosmic Dust 2` | 0.37 ms/frame | 3.63 ms/frame | 0.30 ms/frame |
| 26 | `Krash + Illusion - Spiral Movement` | 0.36 ms/frame | 3.68 ms/frame | 0.30 ms/frame |
| 27 | `Geiss - Cauldron - painterly 2 (saturation remix)` | 0.35 ms/frame | 3.51 ms/frame | 0.30 ms/frame |
| 28 | `$$$ Royal - Mashup (431)` | 0.33 ms/frame | 3.98 ms/frame | 0.30 ms/frame |
| 29 | `Geiss - Reaction Diffusion 2` | 0.33 ms/frame | 3.78 ms/frame | 0.30 ms/frame |
| 30 | `Geiss - Spiral Artifact` | 0.31 ms/frame | 3.60 ms/frame | 0.30 ms/frame |

The ordering below roughly rank 20 is within measurement noise. Use the top
group as the meaningful CPU ranking; do not treat adjacent tail entries as
significantly different.

## CPU-only ordering difference

When shader work is forced onto the CPU, `Martin - QBikal - Surface Turbulence
IIb` is the most expensive preset, ahead of `martin - witchcraft reloaded` and
`martin - extreme heat`. The top three are the same set under both metrics:

1. `Martin - QBikal - Surface Turbulence IIb` — 9.00 ms/frame
2. `martin - witchcraft reloaded` — 8.24 ms/frame
3. `martin - extreme heat` — 7.17 ms/frame

`Surface Turbulence IIb` is CPU-heavy but GPU-light: it renders in 2.30 ms on
the M1 but consumes the most CPU-only time. `extreme heat` and `witchcraft
reloaded` are expensive on both sides.

## Why the top effects are expensive

1. **`martin - witchcraft reloaded`** — ~3.6 KB composite shader, ~4.2 KB of
   frame equations, 2 custom waves (1024 vertices/frame), and a custom shape.
   Highest combined CPU and render cost.
2. **`Martin - QBikal - Surface Turbulence IIb`** — 4 custom waves × 506
   samples = **2024 waveform vertices per frame**, plus ~4.3 KB of frame
   equations and a composite-shader loop. Highest CPU-only load.
3. **`martin - extreme heat`** — ~8.2 KB of warp/composite GLSL, **31 texture
   reads**, ~3.1 KB of frame equations, and a 160-sample dotted wave. Ties for
   the slowest render call.
4. **`Flexi - alien fish pond`** — 23 shader texture reads, ~3.2 KB of shader
   code, and ~8 KB of frame equations.
5. **`flexi - bouncing balls [double mindblob neon mix]`** — 96 polygon sides
   across 2 shapes, 21 texture reads, ~7.3 KB of frame equations, and ~1.1 KB
   of pixel equations.

The upstream Mandelbox explorer/raymarching preset is deliberately excluded
from the bundled preset pack, so it is not part of this ranking.
