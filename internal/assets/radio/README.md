# Initial radio catalogue

`stations.json` is derived from the country M3U playlists in
[AlonDrilich/radio-playlists](https://github.com/AlonDrilich/radio-playlists),
revision `2115163a9da8a5fd6c1046943f58d065d2a5bba8` (generated 2026-10-17).
Each entry retains its station name, stream URL and country playlist name.
URL fragments (player filename hints, not sent in HTTP requests) are removed.
Duplicate stream URLs are included once. Genre and top lists would duplicate
these entries and are not imported.

The upstream playlist data is CC0 1.0, derived from public-domain Radio Browser
data. The full upstream license is in `LICENSE-DATA`. Audio remains hosted and
owned by its respective stations. Inclusion does not guarantee availability or
browser codec support.

Regenerate with `scripts/import-radio-stations.py` using a GitHub source tarball
for the desired revision, and update this attribution. The bundled snapshot is
inserted transactionally once per database, tracked by `go_settings.radio_seed_v1`.
Restarts neither download data nor overwrite personal edits or hidden stations.

`additions.json` contains curated stations kept separately from the upstream
snapshot, including **Radio 666** (France). These are embedded in the executable
and applied to existing databases as well as new ones. Each addition has a stable
key recorded in `go_settings`; repeat startup does not duplicate it or restore a
station hidden by an account. Matching shared stream URLs are not inserted twice.
Regenerating the upstream snapshot leaves these additions intact.
