import subprocess
import unittest
from unittest.mock import patch

import aws_exhibition_feature_deploy as feature


class FeatureDeploymentTests(unittest.TestCase):
    def test_ref_requires_pushed_feature_commit(self):
        sha = "a" * 40
        branch = "codex/auto-explicit-cache"
        with patch.object(feature.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, sha + "\trefs/heads/" + branch + "\n")):
            feature.check_source(branch, sha)
        with patch.object(feature.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, "b" * 40 + "\trefs/heads/" + branch + "\n")):
            with self.assertRaises(ValueError):
                feature.check_source(branch, sha)

    def test_dev_and_invalid_revisions_rejected_before_dispatch(self):
        for branch, sha in [("dev", "a" * 40), ("main", "a" * 40), ("codex/test", "short")]:
            with self.subTest(branch=branch), self.assertRaises(ValueError):
                feature.check_source(branch, sha)

    def test_target_is_ohio_modelhub_only(self):
        service = {"status": "ACTIVE", "pendingCount": 0, "runningCount": 2, "desiredCount": 2,
                   "deployments": [{"rolloutState": "COMPLETED"}], "taskDefinition": "old"}
        with patch.object(feature.deployment, "aws_cli", side_effect=[{"Account": "054043816891"}, {"services": [service]}]) as call:
            self.assertEqual(feature.check_target(), "old")
            self.assertEqual(call.call_args.args, ("ecs", "describe-services", "--cluster", "tomirro", "--services", "modelhub-service"))
        self.assertEqual(feature.deployment.REGION, "us-east-2")
        self.assertEqual(feature.deployment.PIPELINE, "tomirro-modelhub")


if __name__ == "__main__":
    unittest.main()
