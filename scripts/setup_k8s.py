#!/usr/bin/env python3
"""Create Kubernetes Secrets directly via kubectl, keeping existing credentials."""
import argparse
import base64
import json
import secrets
import shlex
import subprocess
from urllib.parse import urlsplit


SERVICE_KEYS = {
    'postgres-secret': {'POSTGRES_PASSWORD', 'DATABASE_URL'},
    'fileserver-secret': {'DATABASE_URL'},
    's3-secret': {'S3_ACCESS_KEY', 'S3_SECRET_KEY'},
}


def manifest(name, namespace, data):
    return {'apiVersion': 'v1', 'kind': 'Secret',
            'metadata': {'name': name, 'namespace': namespace},
            'type': 'Opaque', 'stringData': data}


def new_bundle(password=None):
    password = password or secrets.token_hex(32)
    values = {
        'postgres-secret': {
            'POSTGRES_PASSWORD': password,
            'DATABASE_URL': (
                f'postgres://fileserver:{password}'
                '@127.0.0.1:5432/fileserver?sslmode=disable'
            ),
        },
        'fileserver-secret': {
            'DATABASE_URL': f'postgres://fileserver:{password}@postgres:5432/fileserver?sslmode=disable',
        },
        's3-secret': {
            'S3_ACCESS_KEY': secrets.token_hex(16),
            'S3_SECRET_KEY': secrets.token_hex(32),
        },
    }
    return {'apiVersion': 'v1', 'kind': 'List', 'items': [
        manifest(name, 'fileserver', data) for name, data in values.items()
    ]}


def validate_bundle(bundle):
    """Reject inconsistent credentials before creating any missing Secrets."""
    def require(condition):
        if not condition:
            raise ValueError('Invalid bundle')

    try:
        require(bundle['apiVersion'] == 'v1' and bundle['kind'] == 'List')
        items = bundle['items']
        require(len(items) == 3)
        by_name = {item['metadata']['name']: item for item in items}
        require(set(by_name) == set(SERVICE_KEYS))
        for name, item in by_name.items():
            require(item['apiVersion'] == 'v1' and item['kind'] == 'Secret')
            require(item['metadata']['namespace'] == 'fileserver')
            require(item['type'] == 'Opaque')
            data = item['stringData']
            require(set(data) == SERVICE_KEYS[name])
            require(all(isinstance(value, str) and value for value in data.values()))

        postgres = by_name['postgres-secret']['stringData']
        s3 = by_name['s3-secret']['stringData']
        password = postgres['POSTGRES_PASSWORD']
        for value in (password, s3['S3_ACCESS_KEY'], s3['S3_SECRET_KEY']):
            require(len(value) >= 32 and all(c in '0123456789abcdef' for c in value))
        for name, host in (('postgres-secret', '127.0.0.1'),
                           ('fileserver-secret', 'postgres')):
            raw_url = by_name[name]['stringData']['DATABASE_URL']
            require(not any(c.isspace() for c in raw_url))
            url = urlsplit(raw_url)
            require((url.scheme, url.username, url.password, url.hostname,
                     url.port, url.path, url.query, url.fragment) == (
                'postgres', 'fileserver', password, host, 5432,
                '/fileserver', 'sslmode=disable', ''))
    except (KeyError, TypeError, ValueError, AttributeError):
        raise ValueError('Invalid Kubernetes Secrets; credentials were not overwritten.') from None


class Kubectl:
    def __init__(self, command):
        self.command = shlex.split(command)
        if not self.command:
            raise ValueError('kubectl command must not be empty.')

    def run(self, *args, payload=None):
        try:
            result = subprocess.run(
                [*self.command, *args], input=payload if payload is not None else '',
                text=True, capture_output=True, check=True, timeout=120,
            )
        except (OSError, subprocess.SubprocessError):
            # kubectl errors can include the submitted Secret. Never echo them.
            raise ValueError('kubectl failed; check cluster access and permissions, then retry.') from None
        return result.stdout

    def get_secret(self, namespace, name):
        output = self.run('-n', namespace, 'get', 'secret', name,
                          '--ignore-not-found', '-o', 'json')
        if not output.strip():
            return None
        try:
            item = json.loads(output)
            if (item['apiVersion'] != 'v1' or item['kind'] != 'Secret'
                    or item['metadata']['name'] != name
                    or item['metadata']['namespace'] != namespace
                    or item['type'] != 'Opaque'):
                raise ValueError
            data = {key: base64.b64decode(value, validate=True).decode('utf-8')
                    for key, value in item['data'].items()}
            return manifest(name, namespace, data)
        except (KeyError, TypeError, ValueError, AttributeError):
            raise ValueError('Invalid Kubernetes Secret response.') from None

    def create_secret(self, item):
        # create prevents a concurrent setup from overwriting existing credentials.
        self.run('create', '-f', '-', payload=json.dumps(item))


def setup(kubectl):
    existing = {name: kubectl.get_secret('fileserver', name) for name in SERVICE_KEYS}
    password = None
    try:
        if existing['postgres-secret'] is not None:
            password = existing['postgres-secret']['stringData']['POSTGRES_PASSWORD']
        elif existing['fileserver-secret'] is not None:
            password = urlsplit(existing['fileserver-secret']['stringData']['DATABASE_URL']).password
    except (KeyError, TypeError, ValueError):
        raise ValueError('Invalid existing database credentials.') from None
    bundle = new_bundle(password)
    bundle['items'] = [existing[item['metadata']['name']] or item for item in bundle['items']]
    validate_bundle(bundle)
    created = 0
    for item in bundle['items']:
        if existing[item['metadata']['name']] is None:
            kubectl.create_secret(item)
            created += 1
    return created


def setup_telegram(kubectl):
    existing = kubectl.get_secret('observability', 'alertmanager-telegram')
    credentials = existing['stringData'] if existing is not None else {
        'bot_token': input('Telegram bot token: ').strip(),
        'chat_id': input('Telegram Chat ID: ').strip(),
    }
    token = credentials.get('bot_token')
    chat_id = credentials.get('chat_id')
    if (not isinstance(token, str) or not token
            or not isinstance(chat_id, str)
            or not chat_id.removeprefix('-').isascii()
            or not chat_id.removeprefix('-').isdigit()
            or int(chat_id) == 0):
        raise ValueError('Invalid Telegram bot token or Chat ID.')
    if existing is None:
        kubectl.create_secret(manifest('alertmanager-telegram', 'observability', credentials))
        return True
    return False


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--kubectl', required=True, help='kubectl command supplied by Makefile')
    parser.add_argument('--only', choices=('services', 'telegram', 'all'), default='all')
    args = parser.parse_args()
    try:
        kubectl = Kubectl(args.kubectl)
        if args.only in ('services', 'all'):
            count = setup(kubectl)
            print(f'Service Secrets created: {count}; existing credentials kept.')
        if args.only in ('telegram', 'all'):
            created = setup_telegram(kubectl)
            print('Telegram Secret created.' if created else 'Existing Telegram Secret kept.')
    except (OSError, ValueError, EOFError):
        raise SystemExit('Cannot prepare Kubernetes Secrets. Check cluster access, '
                         'permissions and credentials; existing Secrets were not overwritten.') from None


if __name__ == '__main__':
    main()
