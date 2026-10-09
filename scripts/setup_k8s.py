#!/usr/bin/env python3
"""Create private secret.yaml files per service; preserve existing credentials."""
import argparse
import fcntl
import json
import os
from pathlib import Path
import secrets
import tempfile
from urllib.parse import urlsplit

import yaml

ROOT = Path(__file__).resolve().parents[1]
DEFAULT_DIRECTORY = ROOT / 'k8s'
SECRET_PATHS = {
    'postgres-secret': Path('postgres/secret.yaml'),
    'fileserver-secret': Path('fileserver/secret.yaml'),
    's3-secret': Path('s3/secret.yaml'),
}


def new_bundle():
    password = secrets.token_hex(32)
    values = {
        'postgres-secret': {'POSTGRES_PASSWORD': password},
        'fileserver-secret': {
            'DATABASE_URL': f'postgres://fileserver:{password}@postgres:5432/fileserver?sslmode=disable',
        },
        's3-secret': {
            'S3_ACCESS_KEY': secrets.token_hex(16),
            'S3_SECRET_KEY': secrets.token_hex(32),
        },
    }
    return {'apiVersion': 'v1', 'kind': 'List', 'items': [
        {'apiVersion': 'v1', 'kind': 'Secret',
         'metadata': {'name': name, 'namespace': 'fileserver'},
         'type': 'Opaque', 'stringData': data}
        for name, data in values.items()
    ]}


def validate_bundle(bundle):
    """Fail closed on partial/corrupted data instead of replacing live credentials."""
    def require(condition):
        if not condition:
            raise ValueError("Invalid bundle")

    try:
        require(bundle['apiVersion'] == 'v1' and bundle['kind'] == 'List')
        items = bundle['items']
        require(len(items) == 3)
        by_name = {item['metadata']['name']: item for item in items}
        keys = {'postgres-secret': {'POSTGRES_PASSWORD'},
                'fileserver-secret': {'DATABASE_URL'},
                's3-secret': {'S3_ACCESS_KEY', 'S3_SECRET_KEY'}}
        require(set(by_name) == set(keys))
        for name, item in by_name.items():
            require(item['apiVersion'] == 'v1' and item['kind'] == 'Secret')
            require(item['metadata']['namespace'] == 'fileserver')
            require(item['type'] == 'Opaque')
            require(set(item['stringData']) == keys[name])
            require(all(isinstance(v, str) and v for v in item['stringData'].values()))
        password = by_name['postgres-secret']['stringData']['POSTGRES_PASSWORD']
        url = urlsplit(by_name['fileserver-secret']['stringData']['DATABASE_URL'])
        require((url.scheme, url.username, url.password, url.hostname, url.port, url.path, url.query) == (
            'postgres', 'fileserver', password, 'postgres', 5432, '/fileserver', 'sslmode=disable'))
        for name, item in by_name.items():
            if name != 'fileserver-secret':
                for value in item['stringData'].values():
                    require(len(value) >= 32 and all(c in '0123456789abcdef' for c in value))
    except (KeyError, TypeError, ValueError, AttributeError):
        raise ValueError('Invalid Kubernetes secret bundle; restore the original file. '
                         'Credentials were not changed or printed.') from None


def write_secret(path, manifest):
    path.parent.mkdir(parents=True, exist_ok=True)
    descriptor, temporary = tempfile.mkstemp(prefix='.secret-', suffix='.yaml', dir=path.parent)
    try:
        with os.fdopen(descriptor, 'w') as stream:
            yaml.safe_dump(manifest, stream, sort_keys=False)
        # Publish a complete file without overwriting an existing path.
        os.link(temporary, path)
    finally:
        os.unlink(temporary)


def setup(directory):
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    # Serialize the three-file transaction. No persistent lock file is necessary.
    descriptor = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        fcntl.flock(descriptor, fcntl.LOCK_EX)
        return setup_locked(directory)
    finally:
        os.close(descriptor)


def setup_locked(directory):
    legacy = directory / 'generated/secrets.json'
    paths = {name: directory / relative for name, relative in SECRET_PATHS.items()}
    if any(path.is_symlink() for path in [legacy, *paths.values()]):
        raise ValueError('Secret files must not be symbolic links.')
    existing = {name: yaml.safe_load(path.read_text()) for name, path in paths.items() if path.exists()}
    if legacy.exists():
        # Migrate the former format without changing passwords on existing PVCs.
        bundle = json.loads(legacy.read_text())
        validate_bundle(bundle)
        expected = {item['metadata']['name']: item for item in bundle['items']}
        if any(manifest != expected[name] for name, manifest in existing.items()):
            raise ValueError('YAML secrets disagree with the legacy bundle.')
    elif len(existing) == len(paths):
        bundle = {'apiVersion': 'v1', 'kind': 'List', 'items': list(existing.values())}
    elif existing:
        raise ValueError('Incomplete secret set; restore the missing original files.')
    else:
        bundle = new_bundle()
    validate_bundle(bundle)
    if any(manifest['metadata']['name'] != name for name, manifest in existing.items()):
        raise ValueError('Secret manifests are in the wrong service directories.')
    created = len(existing) != len(paths)
    for manifest in bundle['items']:
        name = manifest['metadata']['name']
        if name not in existing:
            write_secret(paths[name], manifest)
        paths[name].chmod(0o600)
    if legacy.exists():
        legacy.unlink()
        # The old directory may contain unrelated files; leave those untouched.
        if not any(legacy.parent.iterdir()):
            legacy.parent.rmdir()
    return created


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, default=DEFAULT_DIRECTORY)
    args = parser.parse_args()
    try:
        created = setup(args.directory)
    except (OSError, ValueError, yaml.YAMLError):
        # JSON decoder errors and OS errors can contain input; do not echo them.
        raise SystemExit('Cannot create or validate Kubernetes secrets. Check permissions '
                         'or restore the original files; existing credentials were not overwritten.') from None
    print('Service secret.yaml files created (0600).' if created else 'Existing YAML secrets validated and kept.')


if __name__ == '__main__':
    main()
