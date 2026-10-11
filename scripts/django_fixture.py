"""Build a test-owned database with the original Django migrations and views.

Usage: ../reinhardt/.venv/bin/python scripts/django_fixture.py /tmp/fixture
This is optional parity-test tooling, never part of the Go application.
"""

import json
import logging
import os
import sqlite3
from pathlib import Path
import sys
from unittest.mock import patch
from urllib.parse import urlencode

destination = Path(sys.argv[1]).resolve()
destination.mkdir(parents=True, exist_ok=True)
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "reinhardt"))
os.environ["DJANGO_SETTINGS_MODULE"] = "reinhardt.test_settings"
os.environ["BASE_DIR"] = str(destination)
os.environ["SECRET_KEY"] = "parity-secret"
os.environ["LANGUAGE_CODE"] = "en-us"
os.environ["URL_PREFIX"] = ""
logging.disable(logging.INFO)

with patch("pathlib.Path.home", return_value=destination):
    from django.conf import settings

    settings.DATABASES = {
        "default": {
            "ENGINE": "django.db.backends.sqlite3",
            "NAME": destination / "django.sqlite3",
        }
    }
    settings.ALLOWED_HOSTS = ["testserver"]
    import django

    django.setup()
    from django.core.management import call_command
    from django.db import connection, transaction
    from django.contrib.auth.models import User
    from django.test import Client
    from django.utils import translation
    from reinhardt.templatetags.reinhardt_extras import suggest_data
    from reinhardt.models import MusicDirectory, MusicFile, MusicFileTag, Preferences
    from reinhardt.helpers import add_music_file_ids_to_tag
    from reinhardt.fts import fts_index_music_files

    call_command("migrate", verbosity=0)
    user = User.objects.create(
        username="alice", email="alice@example.com", is_superuser=True
    )
    music = destination / "music"
    music.mkdir(exist_ok=True)
    directory = MusicDirectory.objects.create(local_path=str(music))
    for position, title in enumerate(
        ["First tune", "Second tune", "Third tune"], start=1
    ):
        path = f"{position}.mp3"
        (music / path).write_bytes(b"abcdefghijklmnopqrstuvwxyz")
        MusicFile.objects.create(
            music_directory=directory,
            relative_path=path,
            artist="Artist" if position < 3 else "Another",
            title=title,
            album="Album" if position < 3 else "Other",
            genre="jazz",
            year="2001",
            comment="Comment",
            duration=10,
            track_number=position,
        )
    add_music_file_ids_to_tag([1, 2], user, "queue")
    add_music_file_ids_to_tag([2, 1], user, "my playlist")
    from reinhardt.models import Tag

    shared_tag = Tag.objects.get(owner=user, label="my playlist")
    shared_tag.is_shared = True
    shared_tag.save()
    Preferences.objects.create(owner=user, access_token="django-token")
    fts_index_music_files()
    client = Client()
    client.force_login(user)
    paths = [
        "/",
        "/artists/",
        "/artists/?q=A",
        "/genres/",
        "/artist/?q=Artist",
        "/album/?q=Album",
        "/genre/?q=jazz",
        "/search/?sq=tune",
        "/search/",
        "/queue/",
        "/tags/",
        "/tag/?q=2",
        "/preferences/",
        "/favorites/",
        "/music_file_dropdown/?q=1",
        "/music_file_tagger/?q=1",
        "/artist/add_to_tag/?artist=Artist",
        "/queue/add_to_tag/",
        "/search/add_to_tag/?sq=tune",
        "/track_details_modal/1",
        "/api/music_files/search/?sq=tune&limit=1&offset=1&order_by=title",
        "/accounts/login/",
        f"/shared/{shared_tag.pk}/{shared_tag.share_hash()}/",
        "/friends/",
        "/ytrip/",
        "/ytrip/failures/",
        "/upload/",
        "/album/?q=absent",
        "/artist/?q=absent",
        "/search/?sq=absent",
    ]
    fixtures = []
    total = 0

    def save_fixture(name, fixture_paths):
        global total
        # Each state represents a fresh application process, without the
        # original helper's process-wide suggestion cache from an earlier state.
        suggest_data.cache_clear()
        fixture = destination / name
        fixture.mkdir()
        snapshots = []
        for language in ["en-us", "fr-fr"]:
            with translation.override(language):
                for path in fixture_paths:
                    for fragment in [False, True]:
                        response = client.get(
                            path, **({"HTTP_X_UP_TARGET": "main"} if fragment else {})
                        )
                        assert response.status_code == 200, (
                            name,
                            language,
                            path,
                            response.status_code,
                        )
                        snapshots.append(
                            {
                                "path": path,
                                "fragment": fragment,
                                "language": language,
                                "body": response.content.decode(),
                            }
                        )
        with sqlite3.connect(fixture / "django.sqlite3") as backup:
            connection.connection.backup(backup)
        if name == "populated":
            queue = list(
                MusicFileTag.objects.filter(
                    tag__owner=user, tag__label="queue"
                ).order_by("rank", "pk")
            )
            cases = [
                ("first", 0, {}, False),
                ("advance", queue[0].pk, {}, False),
                ("select-existing", queue[-1].pk, {"list_item": "1"}, False),
                ("enqueue-new", queue[-1].pk, {"next_track": "3"}, False),
                ("requeue-existing", queue[0].pk, {"next_track": "1"}, False),
                (
                    "selection-precedence",
                    queue[-1].pk,
                    {"list_item": "1", "next_track": "3"},
                    False,
                ),
                ("exhausted", queue[-1].pk, {}, False),
                ("empty", queue[-1].pk, {}, True),
            ]
            playback = []
            for label, position, form, empty in cases:
                setup = [
                    {
                        "sql": "UPDATE reinhardt_preferences SET current_position=?,autofeed_queue=0,autofeed_similar=0 WHERE owner_id=1",
                        "args": [position],
                    }
                ]
                if empty:
                    setup.append(
                        {
                            "sql": "DELETE FROM reinhardt_musicfiletag WHERE tag_id IN (SELECT id FROM reinhardt_tag WHERE owner_id=1 AND label='queue')",
                            "args": [],
                        }
                    )
                for language in ["en-us", "fr-fr"]:
                    for fragment in [False, True]:
                        with transaction.atomic(), translation.override(language):
                            with connection.cursor() as cursor:
                                for query in setup:
                                    cursor.execute(
                                        query["sql"].replace("?", "%s"), query["args"]
                                    )
                            response = client.post(
                                "/next_song/",
                                form,
                                **(
                                    {"HTTP_X_UP_TARGET": "#now_playing"}
                                    if fragment
                                    else {}
                                ),
                            )
                            assert response.status_code == 200, (
                                label,
                                response.status_code,
                            )
                            prefs = Preferences.objects.get(owner=user)
                            items = list(
                                MusicFileTag.objects.filter(
                                    tag__owner=user, tag__label="queue"
                                )
                                .order_by("rank", "pk")
                                .values("id", "music_file_id", "rank")
                            )
                            playback.append(
                                {
                                    "name": label,
                                    "language": language,
                                    "fragment": fragment,
                                    "setup": setup,
                                    "form": form,
                                    "body": response.content.decode(),
                                    "position": prefs.current_position,
                                    "queue": items,
                                }
                            )
                            transaction.set_rollback(True)
            (fixture / "playback.json").write_text(json.dumps(playback))
            total += len(playback)
        (fixture / "responses.json").write_text(json.dumps(snapshots))
        fixtures.append(name)
        total += len(snapshots)

    save_fixture("populated", paths)
    artist = "O'Reilly & <b>Friends</b> / plus + question?"
    album = 'Album "quoted" & <tracks>'
    MusicFile.objects.filter(pk__in=[1, 2]).update(
        artist=artist, album=album, title='A "quoted" tune & <notes>'
    )
    Tag.objects.filter(pk=shared_tag.pk).update(label="o'reilly & <playlist>")
    fts_index_music_files()
    escaped_paths = paths + [
        "/artist/?" + urlencode({"q": artist}),
        "/album/?" + urlencode({"q": album}),
    ]
    save_fixture("escaped", escaped_paths)
    MusicFile.objects.all().delete()
    fts_index_music_files()
    empty_paths = [
        p
        for p in paths
        if not any(
            x in p
            for x in ("music_file_dropdown", "music_file_tagger", "track_details_modal")
        )
    ]
    save_fixture("empty", empty_paths)
    (destination / "fixtures.json").write_text(json.dumps(fixtures))
    print(
        f"Created {len(fixtures)} Django databases and {total} response snapshots in {destination}"
    )
