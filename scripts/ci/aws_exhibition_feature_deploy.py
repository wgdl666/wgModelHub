"""Explicit Ohio-only feature release through the existing managed pipeline.

No GitHub workflow, dev merge, pipeline definition change, or CN release.
The caller must explicitly choose a pushed feature branch and its exact SHA.
"""

import argparse
import json
from pathlib import Path
import re
import subprocess
import uuid

import aws_exhibition_deploy as deployment


def check_source(branch, commit):
    if branch in {"dev", "main", "master"} or not branch or branch.startswith("-"):
        raise ValueError("an explicit feature branch is required")
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("a full source commit SHA is required")
    subprocess.run(["git", "check-ref-format", "refs/heads/" + branch], check=True, capture_output=True)
    result = subprocess.run(
        ["git", "ls-remote", "--heads", "origin", "refs/heads/" + branch],
        check=True, capture_output=True, text=True, timeout=30,
    )
    if result.stdout.strip() != commit + "\trefs/heads/" + branch:
        raise ValueError("remote feature branch does not match the selected commit")


def check_target():
    identity = deployment.aws_cli("sts", "get-caller-identity")
    if identity["Account"] != "054043816891":
        raise ValueError("unexpected AWS account")
    result = deployment.aws_cli("ecs", "describe-services", "--cluster", "tomirro", "--services", "modelhub-service")
    services = result.get("services", [])
    if result.get("failures") or len(services) != 1:
        raise ValueError("Ohio ModelHub service not found")
    service = services[0]
    if service["status"] != "ACTIVE" or service["pendingCount"] or service["runningCount"] != service["desiredCount"]:
        raise ValueError("wait for Ohio ModelHub to become stable")
    if len(service["deployments"]) != 1 or service["deployments"][0].get("rolloutState") != "COMPLETED":
        raise ValueError("another Ohio deployment is in progress")
    return service["taskDefinition"]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--branch", required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--request-id", default=None, help="reuse this ID when recovering an uncertain dispatch")
    args = parser.parse_args()
    check_source(args.branch, args.commit)
    previous = check_target()
    request_id = args.request_id or str(uuid.uuid4())
    execution = deployment.start_execution(args.commit, "feature-" + request_id)
    path = Path("artifacts/aws-exhibition-feature-deployment.json")
    path.parent.mkdir(exist_ok=True)
    record = {"pipeline": deployment.PIPELINE, "region": deployment.REGION,
              "branch": args.branch, "sourceSha": args.commit, "executionId": execution,
              "requestId": request_id, "previousTaskDefinition": previous, "status": "InProgress"}
    path.write_text(json.dumps(record, indent=2) + "\n")
    print("Ohio feature execution: " + execution, flush=True)
    record.update(deployment.wait(args.commit, execution))
    path.write_text(json.dumps(record, indent=2) + "\n")
    print("Ohio feature deployment: " + record["status"], flush=True)


if __name__ == "__main__":
    main()
