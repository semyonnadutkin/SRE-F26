import concurrent.futures
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import yaml

from setup_k8s import SECRET_PATHS, new_bundle, setup, setup_telegram, validate_bundle


def read_bundle(directory):
    return {'apiVersion': 'v1', 'kind': 'List', 'items': [
        yaml.safe_load((directory / path).read_text()) for path in SECRET_PATHS.values()
    ]}


class KubernetesSecretTests(unittest.TestCase):
    def test_credentials_agree_and_are_not_shared_between_installations(self):
        first, second = new_bundle(), new_bundle()
        validate_bundle(first)
        validate_bundle(second)
        for left, right in zip(first['items'], second['items']):
            self.assertNotEqual(left['stringData'], right['stringData'])

    def test_repeat_setup_preserves_private_yaml_files(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.assertTrue(setup(root))
            original = {p: (root / p).read_bytes() for p in SECRET_PATHS.values()}
            self.assertFalse(setup(root))
            validate_bundle(read_bundle(root))
            for path, content in original.items():
                self.assertEqual((root / path).read_bytes(), content)
                self.assertEqual((root / path).stat().st_mode & 0o777, 0o600)
                self.assertTrue(content.startswith(b'apiVersion: v1\nkind: Secret\n'))

    def test_concurrent_first_setup_writes_exactly_once(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with concurrent.futures.ThreadPoolExecutor(max_workers=8) as pool:
                self.assertEqual(sum(pool.map(setup, [root] * 8)), 1)
            validate_bundle(read_bundle(root))
            self.assertEqual(len(list(root.rglob('*.yaml'))), 3)

    def test_legacy_migration_preserves_all_credentials_and_removes_json(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            legacy = root / 'generated/secrets.json'
            legacy.parent.mkdir()
            bundle = new_bundle()
            legacy.write_text(json.dumps(bundle))
            self.assertTrue(setup(root))
            self.assertEqual(read_bundle(root), bundle)
            self.assertFalse(legacy.exists())

    def test_partial_legacy_migration_can_resume(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            legacy = root / 'generated/secrets.json'
            legacy.parent.mkdir()
            bundle = new_bundle()
            legacy.write_text(json.dumps(bundle))
            path = root / 'postgres/secret.yaml'
            path.parent.mkdir()
            path.write_text(yaml.safe_dump(bundle['items'][0], sort_keys=False))
            self.assertTrue(setup(root))
            self.assertEqual(read_bundle(root), bundle)

    def test_missing_yaml_is_not_replaced_with_new_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            setup(root)
            missing = root / 's3/secret.yaml'
            missing.unlink()
            original = (root / 'postgres/secret.yaml').read_bytes()
            with self.assertRaises(ValueError):
                setup(root)
            self.assertFalse(missing.exists())
            self.assertEqual((root / 'postgres/secret.yaml').read_bytes(), original)

    def test_corruption_and_mismatched_password_are_not_silently_replaced(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            setup(root)
            path = root / 'postgres/secret.yaml'
            bad = yaml.safe_load(path.read_text())
            bad['stringData']['POSTGRES_PASSWORD'] = 'a' * 64
            for content in ('{broken', yaml.safe_dump(bad), '{}'):
                path.write_text(content)
                with self.assertRaises((ValueError, yaml.YAMLError)):
                    setup(root)
                self.assertEqual(path.read_text(), content)

    def test_symlink_is_rejected_without_modifying_target(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 's3/secret.yaml'
            path.parent.mkdir()
            target = root / 'target.yaml'
            target.write_text('unchanged')
            path.symlink_to(target)
            with self.assertRaises(ValueError):
                setup(root)
            self.assertEqual(target.read_text(), 'unchanged')

    def test_telegram_secret_created_with_correct_data_and_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch('builtins.input', side_effect=['123456:bot-token', '-1001234567890']) as prompt:
                setup_telegram(root)
            self.assertEqual(prompt.call_count, 2)

            path = root / 'observability/alertmanager/secret.yaml'
            self.assertEqual(yaml.safe_load(path.read_text()), {
                'apiVersion': 'v1',
                'kind': 'Secret',
                'metadata': {
                    'name': 'alertmanager-telegram',
                    'namespace': 'observability',
                },
                'type': 'Opaque',
                'stringData': {
                    'bot_token': '123456:bot-token',
                    'chat_id': '-1001234567890',
                },
            })
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_telegram_secret_reused_without_prompt_or_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with patch('builtins.input', side_effect=['123456:bot-token', '123456789']):
                setup_telegram(root)
            path = root / 'observability/alertmanager/secret.yaml'
            original = path.read_bytes()
            path.chmod(0o644)

            with patch('builtins.input', side_effect=AssertionError('Unexpected prompt')):
                setup_telegram(root)

            self.assertEqual(path.read_bytes(), original)
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_invalid_telegram_credentials_do_not_create_secret(self):
        for token, chat_id in [('', '123'), ('123456:token', ''),
                               ('123456:token', 'abc'), ('123456:token', '0')]:
            with self.subTest(token=token, chat_id=chat_id):
                with tempfile.TemporaryDirectory() as directory:
                    root = Path(directory)
                    with patch('builtins.input', side_effect=[token, chat_id]):
                        with self.assertRaises(ValueError):
                            setup_telegram(root)
                    self.assertFalse((root / 'observability/alertmanager/secret.yaml').exists())

    def test_invalid_existing_telegram_secret_is_not_overwritten(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'observability/alertmanager/secret.yaml'
            path.parent.mkdir(parents=True)
            path.write_text('kind: ConfigMap\n')
            with patch('builtins.input', side_effect=AssertionError('Unexpected prompt')):
                with self.assertRaises(ValueError):
                    setup_telegram(Path(directory))
            self.assertEqual(path.read_text(), 'kind: ConfigMap\n')


if __name__ == '__main__':
    unittest.main()
