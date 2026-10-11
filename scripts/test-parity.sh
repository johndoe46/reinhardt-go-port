#!/bin/sh
set -eu
fixture_dir=$(mktemp -d /tmp/reinhardt-parity.XXXXXX)
trap 'rm -rf "$fixture_dir"' EXIT HUP INT TERM
django_python=${DJANGO_PYTHON:-../reinhardt/.venv/bin/python}
go_command=${GO:-go}
"$django_python" scripts/django_fixture.py "$fixture_dir"
CGO_ENABLED=0 DJANGO_PARITY_DIR="$fixture_dir" "$go_command" test -mod=vendor ./...
