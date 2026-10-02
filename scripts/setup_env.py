#!/usr/bin/env python3
"""Generate local configuration without silently overwriting an existing file."""
import argparse
import os
from pathlib import Path
import secrets
import shlex
import urllib.parse

ROOT = Path(__file__).resolve().parents[1]
GROUPS = [
    ('HTTP', ['APP_PORT', 'FILESERVER_PORT', 'APP_ORIGIN']),
    ('PostgreSQL', ['POSTGRES_USER', 'POSTGRES_PASSWORD', 'POSTGRES_DB', 'DATABASE_URL']),
    ('Object storage', ['S3_ENDPOINT', 'S3_REGION', 'S3_BUCKET', 'S3_ACCESS_KEY', 'S3_SECRET_KEY']),
    ('Authentication (local HTTP; durations use s, m, h)', ['COOKIE_SECURE', 'SESSION_TTL']),
    ('Upload limits (bytes)', ['MAX_FILE_BYTES']),
]


def read_env(path):
    values = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if not line or line.startswith('#'):
            continue
        if line.startswith('export '):
            line = line[7:]
        key, separator, raw = line.partition('=')
        if not separator:
            raise ValueError('Invalid environment assignment')
        parts = shlex.split(raw, comments=True)
        values[key.strip()] = parts[0] if parts else ''
    return values


def write_env(path, values, overwrite=False):
    # Single quotes keep dollar signs and hashes literal in Compose dotenv files.
    groups = []
    for heading, keys in GROUPS:
        lines = [f'# {heading}']
        for key in keys:
            value = values[key]
            if any(character in value for character in "'\r\n"):
                raise ValueError(f'{key} cannot be written safely; edit the environment manually')
            lines.append(f"{key}='{value}'")
        groups.append('\n'.join(lines))
    content = '\n\n'.join(groups) + '\n'
    flags = os.O_WRONLY | os.O_CREAT | (os.O_TRUNC if overwrite else os.O_EXCL)
    descriptor = os.open(path, flags, 0o600)
    with os.fdopen(descriptor, 'w') as stream:
        stream.write(content)
    path.chmod(0o600)


def local_database_url(values):
    quote = urllib.parse.quote
    return (f"postgres://{quote(values['POSTGRES_USER'], safe='')}:{quote(values['POSTGRES_PASSWORD'], safe='')}"
            f"@postgres:5432/{quote(values['POSTGRES_DB'], safe='')}?sslmode=disable")


def prepare_env(defaults, existing):
    values = {key: existing.get(key) or value for key, value in defaults.items()}
    for key in ['POSTGRES_PASSWORD', 'S3_SECRET_KEY']:
        if not values[key] or values[key].startswith(('change-local-', '[', 'TODO', 'replace-')):
            values[key] = secrets.token_hex(32)
    if not values['S3_ACCESS_KEY']:
        values['S3_ACCESS_KEY'] = secrets.token_hex(16)

    connection = urllib.parse.urlsplit(values['DATABASE_URL'])
    if connection.hostname == 'postgres':
        expected = (values['POSTGRES_USER'], values['POSTGRES_PASSWORD'], values['POSTGRES_DB'])
        actual = (urllib.parse.unquote(connection.username or ''), urllib.parse.unquote(connection.password or ''),
                  urllib.parse.unquote(connection.path.lstrip('/')))
        if actual != expected or (connection.port or 5432) != 5432:
            raise ValueError('PostgreSQL credentials disagree with DATABASE_URL; reconcile them before updating.')
    if not values['DATABASE_URL']:
        values['DATABASE_URL'] = local_database_url(values)
    values.update(APP_PORT='80', APP_ORIGIN='http://localhost', COOKIE_SECURE='false')
    return values


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--update', action='store_true', help='Explicitly update local settings while keeping existing credentials')
    args = parser.parse_args()
    path = ROOT / '.env'
    if path.exists() and not args.update:
        raise SystemExit('.env already exists; kept unchanged. Use --update deliberately or edit it yourself.')

    defaults = read_env(ROOT / '.env.example')
    existing = read_env(path) if path.exists() else {}
    values = prepare_env(defaults, existing)
    write_env(path, values, overwrite=args.update)
    print('.env is populated for http://localhost; credentials were not printed.')


if __name__ == '__main__':
    main()
