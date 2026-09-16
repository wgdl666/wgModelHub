"""Dispatch and follow the exact wgModelHub SHA in the Singapore managed pipeline.

Test/Build 成功后自动放行 ApproveApplicationRelease；DetectChanges 关闭，只接受 GitHub 精确 SHA。
"""

import json
import os
from pathlib import Path
import re
import subprocess
import time


PIPELINE = "wg-dev-modelhub"
REGION = "ap-southeast-1"
TERMINAL_FAILURES = {"Failed", "Superseded", "Stopped", "Stopping", "Cancelled"}
REQUIRED_ACTIONS = (
    "GitHub",
    "Test",
    "Build",
    "ApproveApplicationRelease",
    "ManagedRelease",
)


def aws_cli(*args, output_json=True):
    cmd = ["aws", *args, "--region", REGION, "--no-cli-pager"]
    if output_json:
        cmd.extend(["--output", "json"])
    result = subprocess.run(
        cmd,
        check=False,
        capture_output=True,
        text=True,
        timeout=60,
    )
    if result.returncode != 0:
        raise RuntimeError((result.stderr or result.stdout or "aws failed").strip())
    if not output_json:
        return result.stdout.strip()
    if not result.stdout.strip():
        return {}
    return json.loads(result.stdout)


def start_execution(sha, run_key):
    result = aws_cli(
        "codepipeline",
        "start-pipeline-execution",
        "--name", PIPELINE,
        "--source-revisions", "actionName=GitHub,revisionType=COMMIT_ID,revisionValue=" + sha,
        "--client-request-token", "github-" + run_key,
    )
    return result["pipelineExecutionId"]


def verify_execution(run, sha):
    status = run["status"]
    if status in TERMINAL_FAILURES:
        raise RuntimeError("AWS deployment ended with " + status)
    if status != "Succeeded":
        return False
    revisions = [item.get("revisionId") for item in run.get("artifactRevisions", [])]
    if revisions != [sha]:
        raise RuntimeError("AWS successful execution revision does not match GitHub SHA")
    return True


def stage_action_status(state, execution_id, stage_name, action_name):
    for stage in state.get("stageStates", []):
        if stage.get("stageName") != stage_name:
            continue
        latest = stage.get("latestExecution") or {}
        if latest.get("pipelineExecutionId") != execution_id:
            continue
        for action in stage.get("actionStates", []):
            if action.get("actionName") != action_name:
                continue
            action_latest = action.get("latestExecution") or {}
            return action_latest.get("status"), action_latest.get("token")
    return None, None


def maybe_approve(execution_id, sha):
    """仅在本 execution 的 Test/Build 已成功、审批动作仍 InProgress 时放行。"""
    state = aws_cli("codepipeline", "get-pipeline-state", "--name", PIPELINE)
    test_status, _ = stage_action_status(state, execution_id, "Test", "Test")
    build_status, _ = stage_action_status(state, execution_id, "Build", "Build")
    approval_status, token = stage_action_status(
        state, execution_id, "Release", "ApproveApplicationRelease"
    )
    if approval_status != "InProgress" or not token:
        return False
    if test_status != "Succeeded" or build_status != "Succeeded":
        return False
    summary = "GitHub approved run {}, sha {}".format(
        os.environ.get("GITHUB_RUN_ID", "local"),
        sha,
    )
    aws_cli(
        "codepipeline",
        "put-approval-result",
        "--pipeline-name", PIPELINE,
        "--stage-name", "Release",
        "--action-name", "ApproveApplicationRelease",
        "--token", token,
        "--result", "summary={},status=Approved".format(summary),
        output_json=False,
    )
    print("Approved the AWS release gate after Test and Build succeeded.", flush=True)
    return True


def assert_required_actions(execution_id):
    actions = aws_cli(
        "codepipeline",
        "list-action-executions",
        "--pipeline-name", PIPELINE,
        "--filter", "pipelineExecutionId=" + execution_id,
    )
    details = actions.get("actionExecutionDetails", [])
    for name in REQUIRED_ACTIONS:
        matched = [
            item for item in details
            if item.get("actionName") == name and item.get("status") == "Succeeded"
        ]
        if len(matched) != 1:
            raise RuntimeError(
                "Required pipeline action {!r} did not succeed exactly once".format(name)
            )


def wait(sha, execution_id, timeout=10500):
    deadline = time.monotonic() + timeout
    previous = None
    approved = False
    while time.monotonic() < deadline:
        # start-pipeline-execution 刚返回时 GetPipelineExecution 常 254 NotFound，必须重试，不能当终态失败。
        try:
            run = aws_cli(
                "codepipeline",
                "get-pipeline-execution",
                "--pipeline-name", PIPELINE,
                "--pipeline-execution-id", execution_id,
            )["pipelineExecution"]
        except RuntimeError as exc:
            text = str(exc)
            if "PipelineExecutionNotFoundException" in text or "does not exist" in text.lower():
                print(f"AWS execution {execution_id} not visible yet, retrying", flush=True)
                time.sleep(5)
                continue
            raise
        if run["status"] != previous:
            print(f"AWS execution {execution_id}: {run['status']}", flush=True)
            previous = run["status"]
        revisions = [item.get("revisionId") for item in run.get("artifactRevisions", [])]
        if revisions and revisions != [sha]:
            raise RuntimeError("Pipeline source revision differs from the dispatched SHA")
        if not approved:
            approved = maybe_approve(execution_id, sha)
        if verify_execution(run, sha):
            assert_required_actions(execution_id)
            return {
                "pipeline": PIPELINE,
                "executionId": execution_id,
                "sourceSha": sha,
                "status": run["status"],
                "region": REGION,
                "approvalApplied": approved,
                "requiredActions": list(REQUIRED_ACTIONS),
            }
        time.sleep(15)
    raise TimeoutError("Timed out waiting for the exact GitHub SHA in AWS CodePipeline")


def write_evidence(payload):
    path = Path("artifacts/aws-dev-deployment.json")
    path.parent.mkdir(exist_ok=True)
    path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")


def main():
    sha = os.environ["GITHUB_SHA"]
    if os.environ.get("GITHUB_REF") != "refs/heads/dev" or not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise RuntimeError("AWS dev deployment requires the actual dev ref and full commit SHA")
    if os.environ.get("GITHUB_EVENT_NAME") not in {"push", "workflow_dispatch"}:
        raise RuntimeError("Unsupported deployment event")

    execution_id = start_execution(
        sha,
        os.environ["GITHUB_RUN_ID"] + "-" + os.environ["GITHUB_RUN_ATTEMPT"],
    )
    write_evidence({
        "pipeline": PIPELINE,
        "executionId": execution_id,
        "sourceSha": sha,
        "status": "InProgress",
        "region": REGION,
    })
    result = wait(sha, execution_id)
    write_evidence(result)

    url = f"https://{REGION}.console.aws.amazon.com/codesuite/codepipeline/pipelines/{PIPELINE}/view?region={REGION}"
    with open(os.environ["GITHUB_STEP_SUMMARY"], "a", encoding="utf-8") as summary:
        summary.write(
            "### AWS dev deployment succeeded\n\n"
            f"Commit: `{sha}`\n\n"
            "Service: `wg-dev/modelhub-service`\n\n"
            f"Execution: `{execution_id}`\n\n"
            f"[AWS pipeline]({url})\n"
        )


if __name__ == "__main__":
    main()
