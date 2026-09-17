import unittest
from unittest.mock import patch

import aws_exhibition_deploy as deploy


SHA = "a" * 40
EXECUTION = "11111111-1111-1111-1111-111111111111"


class ExhibitionDeploymentTests(unittest.TestCase):
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
        self.assertEqual(deploy.PIPELINE, "tomirro-modelhub")
        self.assertEqual(deploy.REGION, "us-east-2")
        self.assertNotIn("AgentStream", deploy.REQUIRED_ACTIONS)
        self.assertNotIn("ApproveApplicationRelease", deploy.REQUIRED_ACTIONS)

    def test_wait_retries_when_execution_is_not_visible_yet(self):
        calls = {"n": 0}

        def fake_aws(*args, **kwargs):
            if args[1] == "get-pipeline-execution":
                calls["n"] += 1
                if calls["n"] == 1:
                    raise RuntimeError("An error occurred (PipelineExecutionNotFoundException)")
                return {
                    "pipelineExecution": {
                        "status": "Succeeded",
                        "artifactRevisions": [{"revisionId": SHA}],
                    },
                }
            if args[1] == "list-action-executions":
                return {
                    "actionExecutionDetails": [
                        {"actionName": name, "status": "Succeeded"}
                        for name in deploy.REQUIRED_ACTIONS
                    ],
                }
            raise AssertionError(args)

        with patch.object(deploy, "aws_cli", side_effect=fake_aws), \
                patch.object(deploy.time, "sleep"):
            result = deploy.wait(SHA, EXECUTION)
        self.assertEqual(result["executionId"], EXECUTION)
        self.assertEqual(calls["n"], 2)




if __name__ == "__main__":
    unittest.main()
