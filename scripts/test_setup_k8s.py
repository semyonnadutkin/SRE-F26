import base64
import copy
import io
import json
import subprocess
import unittest
from unittest.mock import patch

from setup_k8s import Kubectl, main, manifest, new_bundle, setup, setup_telegram, validate_bundle


class FakeCluster:
    """In-memory kubectl transport: no subprocesses or cluster connections."""
    def __init__(self, items=()):
        self.items = {(item['metadata']['namespace'], item['metadata']['name']):
                      copy.deepcopy(item) for item in items}
        self.created = []
        self.fail_create = None

    def run(self, command, *, input, **kwargs):
        args = command[3:]  # kubectl --context 'test cluster'
        if args[:1] == ['-n']:
            namespace, name = args[1], args[4]
            assert args[2:4] == ['get', 'secret']
            assert args[5:] == ['--ignore-not-found', '-o', 'json']
            item = copy.deepcopy(self.items.get((namespace, name)))
            if item is None:
                output = ''
            else:
                item['data'] = {key: base64.b64encode(value.encode()).decode()
                                for key, value in item.pop('stringData').items()}
                output = json.dumps(item)
        else:
            assert args == ['create', '-f', '-']
            item = json.loads(input)
            key = (item['metadata']['namespace'], item['metadata']['name'])
            if key in self.items or key[1] == self.fail_create:
                raise subprocess.CalledProcessError(1, command, stderr=input)
            self.items[key] = item
            self.created.append(item)
            output = 'secret created\n'
        return subprocess.CompletedProcess(command, 0, stdout=output, stderr='')


class KubernetesSecretTests(unittest.TestCase):
    def setUp(self):
        self.cluster = FakeCluster()
        self.transport = patch('setup_k8s.subprocess.run', side_effect=self.cluster.run).start()
        self.addCleanup(patch.stopall)
        self.kubectl = Kubectl('kubectl --context "test cluster"')

    def test_credentials_agree_and_are_not_shared_between_installations(self):
        first, second = new_bundle(), new_bundle()
        validate_bundle(first)
        validate_bundle(second)
        for left, right in zip(first['items'], second['items']):
            self.assertNotEqual(left['stringData'], right['stringData'])

    def test_first_setup_sends_secrets_only_through_stdin(self):
        self.assertEqual(setup(self.kubectl), 3)
        validate_bundle({'apiVersion': 'v1', 'kind': 'List', 'items': self.cluster.created})
        for call in self.transport.call_args_list:
            command = call.args[0]
            self.assertEqual(command[:3], ['kubectl', '--context', 'test cluster'])
            self.assertNotIn('shell', call.kwargs)
            self.assertTrue(call.kwargs['capture_output'])
            for item in self.cluster.created:
                for value in item['stringData'].values():
                    self.assertNotIn(value, ' '.join(command))

    def test_repeat_setup_keeps_all_credentials_and_does_not_write(self):
        setup(self.kubectl)
        original = copy.deepcopy(self.cluster.items)
        self.cluster.created.clear()
        self.assertEqual(setup(self.kubectl), 0)
        self.assertEqual(self.cluster.items, original)
        self.assertEqual(self.cluster.created, [])

    def test_interrupted_setup_resumes_with_original_database_password(self):
        self.cluster.fail_create = 'fileserver-secret'
        with self.assertRaises(ValueError):
            setup(self.kubectl)
        postgres = copy.deepcopy(self.cluster.items[('fileserver', 'postgres-secret')])
        self.cluster.fail_create = None
        self.cluster.created.clear()
        self.assertEqual(setup(self.kubectl), 2)
        self.assertEqual(self.cluster.items[('fileserver', 'postgres-secret')], postgres)
        validate_bundle({'apiVersion': 'v1', 'kind': 'List',
                         'items': list(self.cluster.items.values())})

    def test_missing_postgres_secret_uses_existing_fileserver_credentials(self):
        setup(self.kubectl)
        original = self.cluster.items.pop(('fileserver', 'postgres-secret'))
        self.assertEqual(setup(self.kubectl), 1)
        self.assertEqual(self.cluster.items[('fileserver', 'postgres-secret')], original)

    def test_mismatched_credentials_are_rejected_before_writes(self):
        setup(self.kubectl)
        self.cluster.items[('fileserver', 'postgres-secret')]['stringData']['POSTGRES_PASSWORD'] = 'a' * 64
        self.cluster.created.clear()
        with self.assertRaises(ValueError):
            setup(self.kubectl)
        self.assertEqual(self.cluster.created, [])

    def test_access_error_is_not_treated_as_missing_secret(self):
        self.transport.side_effect = subprocess.CalledProcessError(1, ['kubectl'], stderr='Forbidden')
        with self.assertRaises(ValueError):
            setup(self.kubectl)
        self.assertEqual(self.transport.call_count, 1)
        self.assertEqual(self.cluster.created, [])

    def test_create_conflict_does_not_overwrite_existing_secret(self):
        item = new_bundle()['items'][0]
        self.cluster.items[('fileserver', 'postgres-secret')] = item
        with self.assertRaises(ValueError):
            self.kubectl.create_secret(new_bundle()['items'][0])
        self.assertEqual(self.cluster.items[('fileserver', 'postgres-secret')], item)
        self.assertEqual(self.cluster.created, [])

    def test_kubectl_failures_do_not_expose_secret_data(self):
        sensitive = 'private-token-that-must-not-be-printed'
        errors = [
            subprocess.CalledProcessError(1, ['kubectl'], output=sensitive, stderr=sensitive),
            subprocess.TimeoutExpired(['kubectl'], 120, output=sensitive, stderr=sensitive),
            FileNotFoundError(sensitive),
        ]
        for error in errors:
            with self.subTest(error=type(error).__name__):
                self.transport.side_effect = error
                with self.assertRaises(ValueError) as caught:
                    self.kubectl.create_secret(manifest('test', 'fileserver', {'token': sensitive}))
                self.assertNotIn(sensitive, str(caught.exception))

    def test_malformed_cluster_response_is_rejected(self):
        item = new_bundle()['items'][0]
        item.pop('stringData')
        item['data'] = {'POSTGRES_PASSWORD': '%%%'}
        for output in ('{broken', 'null', '{}', json.dumps(item)):
            with self.subTest(output=output):
                self.transport.side_effect = None
                self.transport.return_value = subprocess.CompletedProcess([], 0, stdout=output)
                with self.assertRaises(ValueError):
                    self.kubectl.get_secret('fileserver', 'postgres-secret')

    def test_empty_kubectl_command_is_rejected(self):
        with self.assertRaises(ValueError):
            Kubectl('  ')
        self.transport.assert_not_called()

    def test_telegram_secret_created_and_reused_without_prompt(self):
        with patch('builtins.input', side_effect=['123456:bot-token', '-1001234567890']):
            self.assertTrue(setup_telegram(self.kubectl))
        original = copy.deepcopy(self.cluster.items)
        self.assertEqual(self.cluster.created[0], manifest('alertmanager-telegram', 'observability', {
            'bot_token': '123456:bot-token', 'chat_id': '-1001234567890',
        }))
        self.cluster.created.clear()
        with patch('builtins.input', side_effect=AssertionError('Unexpected prompt')):
            self.assertFalse(setup_telegram(self.kubectl))
        self.assertEqual(self.cluster.items, original)
        self.assertEqual(self.cluster.created, [])

    def test_invalid_telegram_credentials_do_not_create_secret(self):
        for token, chat_id in [('', '123'), ('token', ''), ('token', 'abc'),
                               ('token', '0'), ('token', '--123'), ('token', '-0')]:
            with self.subTest(token=token, chat_id=chat_id):
                with patch('builtins.input', side_effect=[token, chat_id]):
                    with self.assertRaises(ValueError):
                        setup_telegram(self.kubectl)
                self.assertEqual(self.cluster.created, [])

    def test_invalid_existing_telegram_secret_is_not_overwritten(self):
        item = manifest('alertmanager-telegram', 'observability', {'bot_token': 'token'})
        self.cluster.items[('observability', 'alertmanager-telegram')] = item
        with patch('builtins.input', side_effect=AssertionError('Unexpected prompt')):
            with self.assertRaises(ValueError):
                setup_telegram(self.kubectl)
        self.assertEqual(self.cluster.created, [])

    def test_main_passes_command_and_runs_both_setups(self):
        with patch('sys.argv', ['setup_k8s.py', '--kubectl', 'kubectl --context "test cluster"']), \
                patch('builtins.input', side_effect=['token', '-123']), \
                patch('sys.stdout', new_callable=io.StringIO) as output:
            main()
        self.assertEqual(len(self.cluster.created), 4)
        self.assertIn('Service Secrets created: 3', output.getvalue())
        self.assertNotIn('token', output.getvalue())

    def test_telegram_only_does_not_access_service_secrets(self):
        with patch('sys.argv', ['setup_k8s.py', '--kubectl', 'kubectl --context "test cluster"',
                                '--only', 'telegram']), \
                patch('builtins.input', side_effect=['token', '-123']), \
                patch('sys.stdout', new_callable=io.StringIO):
            main()
        self.assertEqual(set(self.cluster.items), {('observability', 'alertmanager-telegram')})


if __name__ == '__main__':
    unittest.main()
