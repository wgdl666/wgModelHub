import unittest
from unittest.mock import patch

import aws_dev_deploy as deploy


SHA = "a" * 40
EXECUTION = "11111111-1111-1111-1111-111111111111"


class DeploymentTests(unittest.TestCase):
    def test_success_requires_exact_source_revision(self):
        self.assertTrue(deploy.verify_execution({
            "status": "Succeeded",
            "artifactRevisions": [{"revisionId": SHA}],
        }, SHA))
        with self.assertRaisesRegex(RuntimeError, "revision"):
            deploy.verify_execution({
                "status": "Succeeded",
                "artifactRevisions": [{"revisionId": "b" * 40}],
            }, SHA)

    def test_terminal_failure_is_not_success(self):
        for status in ["Failed", "Superseded", "Stopped", "Stopping", "Cancelled"]:
            with self.subTest(status=status), self.assertRaises(RuntimeError):
                deploy.verify_execution({"status": status}, SHA)

    def test_dispatch_pins_sha_and_idempotency_token(self):
        with patch.object(deploy, "aws_cli", return_value={"pipelineExecutionId": "execution"}) as call:
            self.assertEqual(deploy.start_execution(SHA, "123-1"), "execution")
        args = call.call_args.args
        self.assertIn("actionName=GitHub,revisionType=COMMIT_ID,revisionValue=" + SHA, args)
        self.assertIn("github-123-1", args)
        self.assertEqual(deploy.PIPELINE, "wg-dev-modelhub")
        self.assertEqual(deploy.REGION, "ap-southeast-1")
        self.assertNotIn("AgentStream", deploy.REQUIRED_ACTIONS)

    def test_maybe_approve_requires_matching_execution_and_gates(self):
        state = {
            "stageStates": [
                {
                    "stageName": "Test",
                    "latestExecution": {"pipelineExecutionId": EXECUTION},
                    "actionStates": [{"actionName": "Test", "latestExecution": {"status": "Succeeded"}}],
                },
                {
                    "stageName": "Build",
                    "latestExecution": {"pipelineExecutionId": EXECUTION},
                    "actionStates": [{"actionName": "Build", "latestExecution": {"status": "Succeeded"}}],
                },
                {
                    "stageName": "Release",
                    "latestExecution": {"pipelineExecutionId": EXECUTION},
                    "actionStates": [{
                        "actionName": "ApproveApplicationRelease",
                        "latestExecution": {"status": "InProgress", "token": "tok"},
                    }],
                },
            ],
        }
        with patch.object(deploy, "aws_cli", side_effect=[state, ""]) as call:
            self.assertTrue(deploy.maybe_approve(EXECUTION, SHA))
        self.assertEqual(call.call_args_list[1].args[1], "put-approval-result")


if __name__ == "__main__":
    unittest.main()
