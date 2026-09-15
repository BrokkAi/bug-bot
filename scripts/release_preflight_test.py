import io
import json
import os
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import release_preflight as preflight
import release_authorization as authorization


def archive(payload=b'binary', mode=0o755, mtime=0):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode='w:gz') as tar:
        member = tarfile.TarInfo('bbb')
        member.size, member.mode, member.mtime = len(payload), mode, mtime
        tar.addfile(member, io.BytesIO(payload))
    return output.getvalue()


class ReleasePreflight(unittest.TestCase):
    def test_rebuild_comparison_checks_payload_and_permissions_not_compressor(self):
        self.assertEqual(preflight.snapshot(archive(mtime=1)), preflight.snapshot(archive(mtime=2)))
        self.assertNotEqual(preflight.snapshot(archive()), preflight.snapshot(archive(payload=b'other')))
        self.assertNotEqual(preflight.snapshot(archive()), preflight.snapshot(archive(mode=0o644)))

    def test_missing_wrong_commit_and_failed_runs_are_not_evidence(self):
        sha = 'a' * 40
        cases = [[], [{'head_sha': 'b'*40}], [{
            'head_sha': sha, 'path': preflight.WORKFLOW, 'display_title': 'Release v0.2.2 (publish=false)',
            'id': 1, 'status': 'completed', 'conclusion': 'failure'}]]
        for runs in cases:
            with patch.object(preflight, 'api', return_value={'workflow_runs': runs}):
                with self.assertRaises(ValueError):
                    preflight.remote_run('v0.2.2', sha)

    def test_tag_conflict_fails(self):
        with patch.object(preflight, 'api', return_value={'object': {'type': 'commit', 'sha': 'b'*40}}):
            with self.assertRaisesRegex(ValueError, 'different commit'):
                preflight.tag_version('v0.2.2', 'a'*40)

    def test_publication_failure_before_auth_causes_no_writes(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version', side_effect=ValueError('conflict')), \
                patch.object(preflight, 'gh') as gh, patch.object(authorization, 'github') as auth:
            with self.assertRaisesRegex(ValueError, 'conflict'):
                preflight.publish('v0.2.2', 'a'*40)
            gh.assert_not_called()
            auth.assert_not_called()

    def test_authorization_failure_prevents_any_final_upload(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version'), patch.object(preflight, 'tag_version'), \
                patch.object(preflight, 'github_version', return_value=None), \
                patch.object(authorization, 'github'), patch.object(authorization, 'npm', side_effect=ValueError('permission')), \
                patch.object(preflight, 'gh') as gh:
            with self.assertRaisesRegex(ValueError, 'permission'):
                preflight.publish('v0.2.2', 'a'*40)
            gh.assert_not_called()

    def test_local_identity_cannot_validate_workflow_credentials(self):
        with patch.dict(os.environ, {}, clear=True):
            with self.assertRaisesRegex(ValueError, 'Actions job'):
                authorization.github()
            with self.assertRaisesRegex(ValueError, 'publish-packages.yml'):
                authorization.npm()

    def test_published_recovery_uses_read_only_verification(self):
        with patch.dict(os.environ, GITHUB_REF='refs/tags/v0.2.2'), \
                patch.object(preflight, 'version'), patch.object(preflight, 'tag_version'), \
                patch.object(authorization, 'github'), patch.object(authorization, 'npm'), \
                patch.object(preflight, 'github_version', return_value={'draft': False}), \
                patch.object(preflight.package_registry, 'run') as registry, patch.object(preflight, 'gh') as gh:
            preflight.publish('v0.2.2', 'a'*40)
            registry.assert_called_once_with('verify', preflight.PACKAGES)
            gh.assert_not_called()
