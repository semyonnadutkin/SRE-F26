import tempfile
from pathlib import Path
import unittest
import urllib.parse

from setup_env import GROUPS, ROOT, prepare_env, read_env, write_env


class SetupSafetyTests(unittest.TestCase):
    def test_existing_environment_is_not_overwritten(self):
        values = {key: 'value' for _, keys in GROUPS for key in keys}
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / '.env'
            write_env(path, values)
            with self.assertRaises(FileExistsError):
                write_env(path, dict(values, POSTGRES_PASSWORD='different'))
            self.assertEqual(read_env(path), values)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_secret_characters_remain_literal(self):
        values = {key: 'value' for _, keys in GROUPS for key in keys}
        values['POSTGRES_PASSWORD'] = 'keep$hash#literal:@'
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / '.env'
            write_env(path, values)
            self.assertEqual(read_env(path)['POSTGRES_PASSWORD'], values['POSTGRES_PASSWORD'])

    def test_connection_url_is_populated_and_encoded(self):
        values = prepare_env(read_env(ROOT / '.env.example'), {'POSTGRES_PASSWORD': 'keep$hash#literal:@'})
        connection = urllib.parse.urlsplit(values['DATABASE_URL'])
        self.assertEqual(urllib.parse.unquote(connection.password), values['POSTGRES_PASSWORD'])
        self.assertEqual(connection.hostname, 'postgres')
        self.assertEqual(connection.port, 5432)
        self.assertEqual(connection.path, '/' + values['POSTGRES_DB'])
        self.assertTrue(values['S3_SECRET_KEY'])

    def test_existing_credentials_and_external_database_are_preserved(self):
        existing = {'POSTGRES_PASSWORD': 'existing-local-secret', 'S3_SECRET_KEY': 'existing-storage-secret',
                    'DATABASE_URL': 'postgres://external.example.test/files?sslmode=verify-full'}
        values = prepare_env(read_env(ROOT / '.env.example'), existing)
        for key, value in existing.items():
            self.assertEqual(values[key], value)

    def test_inconsistent_local_credentials_are_rejected(self):
        with self.assertRaises(ValueError):
            prepare_env(read_env(ROOT / '.env.example'),
                        {'POSTGRES_PASSWORD': 'existing-secret', 'DATABASE_URL': 'postgres://fileserver:different@postgres/fileserver'})


if __name__ == '__main__':
    unittest.main()
