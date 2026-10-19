#!/usr/bin/env python3
"""Build the embedded catalogue from a pinned radio-playlists GitHub tarball.

Download https://codeload.github.com/AlonDrilich/radio-playlists/tar.gz/REVISION,
then run: python3 scripts/import-radio-stations.py /path/to/archive.tar.gz
See internal/assets/radio/README.md for the bundled revision and data license.
"""

import json
from pathlib import Path
import sys
import tarfile
from urllib.parse import urlsplit

target = Path(__file__).resolve().parents[1] / "internal/assets/radio"
target.mkdir(exist_ok=True)
stations = []
seen = set()
with tarfile.open(sys.argv[1]) as archive:
    prefix = archive.getnames()[0].split("/")[0] + "/"
    index = json.load(archive.extractfile(prefix + "index.json"))
    for playlist in index["playlists"]:
        if playlist["type"] != "country":
            continue
        name = None
        for line in archive.extractfile(prefix + playlist["path"]).read().decode("utf-8-sig").splitlines():
            if line.startswith("#EXTINF:"):
                # Attributes can contain commas inside quotes.
                quoted = False
                for position, character in enumerate(line):
                    if character == '"':
                        quoted = not quoted
                    elif character == "," and not quoted:
                        name = line[position + 1:].strip()
                        break
            elif line and not line.startswith("#"):
                # Fragments are never sent to the station (some playlists use
                # them as a player filename hint). Store the actual HTTP URL.
                url = line.strip().split("#", 1)[0]
                parsed = urlsplit(url)
                if not name or parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username or parsed.password:
                    raise ValueError(f"Invalid station in {playlist['path']}: {line}")
                if url not in seen:
                    stations.append({"name": name, "country": playlist["name"], "url": url})
                    seen.add(url)
                name = None
    license_text = archive.extractfile(prefix + "LICENSE-DATA").read()
(target / "stations.json").write_text(json.dumps(stations, ensure_ascii=False, separators=(",", ":")) + "\n")
(target / "LICENSE-DATA").write_bytes(license_text)
print(f"Imported {len(stations)} stations in {len({station['country'] for station in stations})} countries")
