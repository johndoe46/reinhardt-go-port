"""Measure Django dropdown requests against test-owned Go benchmark snapshots.

First export snapshots with BenchmarkMusicFileDropdown and
REINHARDT_HTTP_BENCH_EXPORT=/tmp/reinhardt-http-bench. Then run:
../reinhardt/.venv/bin/python scripts/benchmark_dropdown.py /tmp/reinhardt-http-bench
No live database, network service, or real music library is used.
"""

import json
import logging
from pathlib import Path
import shutil
import statistics
import sys
import tempfile
import time
from unittest.mock import patch


snapshot_dir = Path(sys.argv[1]).resolve()
snapshots = sorted(
    snapshot_dir.glob("tracks-*.sqlite3"), key=lambda p: int(p.stem.split("-")[1])
)
if not snapshots:
    raise SystemExit("No BenchmarkMusicFileDropdown snapshots found")
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "reinhardt"))
logging.disable(logging.CRITICAL)

with tempfile.TemporaryDirectory(prefix="reinhardt-django-http-bench-") as temp:
    root = Path(temp)
    with (
        patch("pathlib.Path.home", return_value=root),
        patch.dict(
            "os.environ",
            {
                "DJANGO_SETTINGS_MODULE": "reinhardt.test_settings",
                "BASE_DIR": str(root),
                "SECRET_KEY": "test-secret",
                "LANGUAGE_CODE": "en-us",
                "TIME_ZONE": "UTC",
                "URL_PREFIX": "",
            },
        ),
    ):
        import django
        from django.conf import settings

        settings.DATABASES = {
            "default": {
                "ENGINE": "django.db.backends.sqlite3",
                "NAME": str(root / "active.sqlite3"),
            }
        }
        settings.CACHES = {
            "default": {
                "BACKEND": "django.core.cache.backends.filebased.FileBasedCache",
                "LOCATION": str(root / "cache"),
            }
        }
        settings.ALLOWED_HOSTS = ["testserver"]
        django.setup()

        from django.contrib.auth.models import User
        from django.contrib.sessions.models import Session
        from django.core.cache import cache
        from django.db import connection
        from django.test import Client

        results = []
        path = "/music_file_dropdown/?q=1&tag=&playing=&context=home"
        for snapshot in snapshots:
            connection.close()
            shutil.copyfile(snapshot, root / "active.sqlite3")
            cache.clear()
            with connection.schema_editor() as editor:
                editor.create_model(Session)
            client = Client()
            client.force_login(User.objects.get(pk=1))
            for mode, headers in [
                ("fetch", {}),
                ("fetch-gzip", {"HTTP_ACCEPT_ENCODING": "gzip"}),
                ("unpoly-header", {"HTTP_X_UP_TARGET": "main"}),
            ]:
                for _ in range(3):
                    assert client.get(path, **headers).status_code == 200
                counters = {"sql": 0, "writes": 0}

                def count_sql(execute, sql, params, many, context):
                    counters["sql"] += 1
                    counters["writes"] += sql.lstrip().split()[0].upper() in {
                        "INSERT",
                        "UPDATE",
                        "DELETE",
                    }
                    return execute(sql, params, many, context)

                timings = []
                with connection.execute_wrapper(count_sql):
                    for _ in range(20):
                        start = time.perf_counter()
                        response = client.get(path, **headers)
                        timings.append((time.perf_counter() - start) * 1000)
                        assert response.status_code == 200
                results.append(
                    {
                        "tracks": int(snapshot.stem.split("-")[1]),
                        "mode": mode,
                        "mean_ms": statistics.mean(timings),
                        "median_ms": statistics.median(timings),
                        "sql_per_request": counters["sql"] / 20,
                        "writes_per_request": counters["writes"] / 20,
                    }
                )
        print(json.dumps(results, indent=2))
