<p align="center"><img src="https://raw.githubusercontent.com/ccvass/swarmex/main/docs/assets/logo.svg" alt="Swarmex" width="400"></p>

[![Test, Build & Deploy](https://github.com/ccvass/swarmex-deployer/actions/workflows/publish.yml/badge.svg)](https://github.com/ccvass/swarmex-deployer/actions/workflows/publish.yml)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

# Swarmex Deployer

Blue/green and canary deployments for Docker Swarm via Traefik traffic weights.

Part of [Swarmex](https://github.com/ccvass/swarmex) — enterprise-grade orchestration for Docker Swarm.

## What It Does

Manages zero-downtime deployments by creating a parallel "green" service and gradually shifting traffic from the old version. Supports both blue/green (instant cutover) and canary (incremental shift) strategies with automatic rollback on error threshold breach.

## Labels

```yaml
deploy:
  labels:
    swarmex.deployer.strategy: "canary"        # Deployment strategy: blue-green | canary
    swarmex.deployer.shift-interval: "60"      # Seconds between traffic shifts
    swarmex.deployer.shift-step: "5"           # Percentage to shift per interval
    swarmex.deployer.error-threshold: "5"      # Error rate % to trigger rollback
    swarmex.deployer.rollback-on-fail: "true"  # Auto-rollback on threshold breach
```

## How It Works

1. Receives a deploy request via `POST /deploy?service=ID&image=NEW`.
2. Creates a green service with 1 replica running the new image.
3. Shifts Traefik weights incrementally (e.g., 5% per 60s) from blue to green.
4. Monitors error rate via Prometheus during the shift.
5. Completes cutover at 100% or auto-rolls back if the error threshold is exceeded.

## Quick Start

```bash
# Trigger a canary deployment
curl -X POST "http://swarmex-deployer:8080/deploy?service=my-app&image=my-app:v2"
```

## Verified

Canary shift completed 0→25→50→75→100% in 40 seconds. Rollback triggered correctly when error threshold was exceeded.

## License

Apache-2.0
