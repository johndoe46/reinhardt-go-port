# Butterchurn fullscreen visualization

These assets are embedded and served locally. They are loaded only when the
fullscreen player selects Butterchurn; no CDN or npm build is required.

## Sources

- `butterchurn-2.6.7.min.js`: unchanged `lib/butterchurn.min.js` from
  https://registry.npmjs.org/butterchurn/-/butterchurn-2.6.7.tgz
  (https://github.com/jberg/butterchurn).
- `presets-2.4.7.json`: 30 converted presets from the `presets/converted/`
  directory of https://registry.npmjs.org/butterchurn-presets/-/butterchurn-presets-2.4.7.tgz
  (https://github.com/jberg/butterchurn-presets), packaged as an array of
  `{name, preset}` records with the original preset data preserved.

The selection keeps the original three presets and adds the upstream minimal
pack, excluding its Mandelbox explorer to avoid that raymarching shader. No
additional texture images are required.

Presets, with their original author credits:

- Flexi, martin + geiss - dedicated to the sherwin maxawow
- Geiss - Spiral Artifact
- Geiss - Cosmic Dust 2
- $$$ Royal - Mashup (197)
- $$$ Royal - Mashup (431)
- _Mig_085
- An AdamFX n Martin Infusion 2 flexi - Why The Sky Looks Diffrent Today - AdamFx n Martin Infusion - Tack Tile Disfunction B
- Eo.S. + Phat - cubetrace - v2
- Flexi - alien fish pond
- flexi - bouncing balls [double mindblob neon mix]
- Flexi - mindblob [shiny mix]
- Flexi, fishbrain, Geiss + Martin - tokamak witchery
- Geiss - Cauldron - painterly 2 (saturation remix)
- Geiss - Reaction Diffusion 2
- Geiss - Thumb Drum
- Idiot - Star Of Annon
- Krash + Illusion - Spiral Movement
- Martin - acid wiring
- martin - chain breaker
- martin - extreme heat
- Martin - QBikal - Surface Turbulence IIb
- martin - reflections on black tiles
- martin - witchcraft reloaded
- martin [shadow harlequins shape code] - fata morgana
- Milk Artist At our Best - FED - SlowFast Ft AdamFX n Martin - HD CosmoFX
- suksma - uninitialized variabowl (hydroponic chronic)
- Unchained & Rovastar - Wormhole Pillars (Hall of Shadows mix)
- Unchained - Rewop
- yin - 191 - Temporal singularities
- Zylot - Paint Spill (Music Reactive Paint Mix)

Both packages use the MIT license; the original notices are included alongside
the assets in `LICENSE.butterchurn` and `LICENSE.butterchurn-presets`.

## Updating

Download the pinned npm archives and verify their SHA-512 integrity values from
the npm package metadata before extracting. Copy the engine distribution
unchanged, retain both license files, and rebuild the selected preset array
from the converted JSON files. Update the versioned filenames and references
in `butterchurn-visualization.js` when changing versions.

Verified archive integrity values:

- butterchurn 2.6.7: `sha512-BJiRA8L0L2+84uoG2SSfkp0kclBuN+vQKf217pK7pMlwEO2ZEg3MtO2/o+l8Qpr8Nbejg8tmL1ZHD1jmhiaaqg==`
- butterchurn-presets 2.4.7: `sha512-4MdM8ripz/VfH1BCldrIKdAc/1ryJFBDvqlyow6Ivo1frwj0H3duzvSMFC7/wIjAjxb1QpwVHVqGqS9uAFKhpg==`
