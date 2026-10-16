# Sub2API Video Task Account Failover

Date: 2026-08-12

## Deployment

- Production host: `186.244.215.254`.
- Runbook folder: `/root/runbook`.
- Compose files: `/root/sub2api/deploy/docker-compose.local.yml`, `/root/sub2api/deploy/backups/v0.1.164-af66c4971850-20260725T140840Z/compose.release.override.yml`, `/root/sub2api/deploy/compose.unified-video.override.yml`.
- Current image: `sub2api:video-task-account-failover-20260812`.
- Image ID: `sha256:118d67e0152afaf581865ddea50934f419c64edded99c720002db1b9c6d07681`.
- Previous image: `sub2api:openai-sub2api-api-v1-20260812`.
- Compose backup: `/root/sub2api/deploy/compose.unified-video.override.yml.bak-20260812T083244Z`.
- Release method: the live container binary was replaced with the new backend build, then the container was committed and recreated from the committed image.

## Change

- `VideoTaskService.Create` now retries same-model account selection when pre-submit validation returns `duration is not supported by /v1/videos`.
- The retry happens before local task persistence.
- Malformed user JSON still fails without retry.
- There is no retry after `repo.Create` or after upstream submission.

## Verification

- `go test -tags=unit ./internal/service -run 'TestVideoTaskServiceCreateRetriesNextAccountOnPreSubmitAdapterMismatch|TestVideoTaskServiceCreateDoesNotRetryUserBadRequest' -count=1` passed.
- `go test -tags=unit ./internal/service -count=1` passed.
- `curl -fsS http://127.0.0.1:8080/health` returned `{"status":"ok"}`.
- `docker inspect sub2api --format '{{.State.Status}}|{{.State.Health.Status}}'` returned `running|healthy`.
- `docker compose -f /root/sub2api/deploy/docker-compose.local.yml -f /root/sub2api/deploy/backups/v0.1.164-af66c4971850-20260725T140840Z/compose.release.override.yml -f /root/sub2api/deploy/compose.unified-video.override.yml ps sub2api` showed `sub2api:video-task-account-failover-20260812`.

## Rollback

- Restore `/root/sub2api/deploy/compose.unified-video.override.yml` from `/root/sub2api/deploy/compose.unified-video.override.yml.bak-20260812T083244Z`.
- Recreate only `sub2api` with the same compose stack.
- Fall back to `sub2api:openai-sub2api-api-v1-20260812` if the new image needs to be removed.
