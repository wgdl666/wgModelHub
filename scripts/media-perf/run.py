#!/usr/bin/env python3
"""Run two short, alternating old/new Docker comparisons, one container per case."""
import argparse
import json
import pathlib
import subprocess

parser = argparse.ArgumentParser()
parser.add_argument("--output", type=pathlib.Path, required=True)
parser.add_argument("--context", default="colima")
args = parser.parse_args()
args.output.parent.mkdir(parents=True, exist_ok=True)
with args.output.open("w") as output:
    for repeat, variants in enumerate((('old', 'new'), ('new', 'old')), 1):
        for scenario in ("multipart", "base64-form", "base64-json", "ledger"):
            for concurrency in (1, 8):
                for variant in variants:
                    command = [
                        "docker", "--context", args.context, "run", "--rm",
                        "--network", "none", "--cpus=1", "--memory=2g",
                        "-e", "GOMAXPROCS=1", "-e", "GOGC=100",
                        f"modelhub-media-perf:{variant}", "-scenario", scenario,
                        "-concurrency", str(concurrency), "-requests", "32",
                    ]
                    result = subprocess.run(command, check=True, capture_output=True, text=True, timeout=60)
                    row = json.loads(result.stdout)
                    row.update(variant=variant, repeat=repeat)
                    output.write(json.dumps(row) + "\n")
                    output.flush()
                    print(f"{repeat} {scenario:12} c={concurrency} {variant}: "
                          f"CPU {row['cpu_ms_per_request']:.2f} ms/req; "
                          f"peak {int(row['container_peak_bytes'])/2**20:.1f} MiB", flush=True)
