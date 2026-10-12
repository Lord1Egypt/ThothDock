# Compatibility results

- **host-candidate**: ThothDock 0.2.0 (Docker Engine API 1.41 compatible; not affiliated with Docker, Inc.) 0.2.0, x86_64, kernel 6.18.40.1-microsoft-standard-WSL2, harness 358de70, started 2026-10-12T18:24:55Z

| ID | Priority | Image | host-candidate |
|---|---|---|---|
| P-CORE | P0 | `alpine:3.22` | PASS (46/46) |
| P-IPC | P0 | `alpine:3.22` | PASS (45/45) |
| G-ALPINE | P0 | `alpine:3.22` | PASS (15/15) |
| G-DEBIAN | P0 | `debian:trixie-slim` | PASS (15/15) |
| G-BUSYBOX | P0 | `busybox:1.37` | PASS (14/14) |
| C-REDIS | P1 | `redis:8-alpine` | PASS (9/9) |
| C-VALKEY | P1 | `valkey/valkey:8-alpine` | PASS (9/9) |
| C-MEMCACHED | P1 | `memcached:1.6-alpine` | PASS (7/7) |
| D-NGINX | P1 | `nginx:1.28-alpine` | PASS (12/12) |
| D-HTTPD | P2 | `httpd:2.4-alpine` | PASS (8/8) |
| D-CADDY | P2 | `caddy:2-alpine` | PASS (7/7) |
| F-PYTHON | P1 | `python:3.13-alpine` | PASS (12/12) |
| F-NODE | P1 | `node:22-alpine` | PASS (7/7) |
| F-GO | P1 | `alpine:3.22` | PASS (12/12) |
| F-JAVA | P2 | `eclipse-temurin:21-jdk-alpine` | PASS (9/9) |
| A-POSTGRES | P1 | `postgres:17-alpine` | PASS (13/13) |
| A-MYSQL | P1 | `mysql:8.4` | PASS (12/12) |
| A-MARIADB | P1 | `mariadb:11.8` | PASS (12/12) |
| B-MONGO8 | P1 | `mongo:8.0.32` | PASS (11/11) |
| B-MONGO7 | P1 | `mongo:7.0` | PASS (11/11) |
| B-COUCHDB | P1 | `couchdb:3.5` | PASS (12/12) |
| E-RABBITMQ | P2 | `rabbitmq:4-management-alpine` | PASS (6/6) |
| E-MOSQUITTO | P2 | `eclipse-mosquitto:2` | PASS (6/6) |
| H-MINIO | P2 | `minio/minio:latest` | BLOCKED (0/0) |
| H-SEAWEEDFS | P2 | `chrislusf/seaweedfs:latest` | PARTIAL (4/5) |
| I-DIND | P2 | `docker:29-dind` | UNSUPPORTED_BY_DESIGN (3/3) |
| I-DOOD | P2 | `docker:29-cli` | UNSUPPORTED_BY_DESIGN (2/2) |
| I-NESTED | P2 | `docker:29-cli` | PASS (3/3) |
| P-UNIX | P0 | `alpine:3.22` | PASS (10/10) |
| P-SQLITE | P0 | `python:3.13-alpine` | PASS (10/10) |
| P-LMDB | P0 | `alpine:3.22` | PASS (4/4) |
| D-PHPFPM | P0 | `php:8.4-fpm-alpine` | PASS (11/11) |
| C-COREDNS | P0 | `coredns/coredns:1.12.1` | PASS (9/9) |
| M-MAILPIT | P0 | `axllent/mailpit:v1.27` | PASS (8/8) |
| P-PROMETHEUS | P0 | `prom/prometheus:v3.5.0` | PASS (9/9) |
| L-FLUENTBIT | P0 | `fluent/fluent-bit:4.0` | PASS (9/9) |
| S-OPENSSH | P0 | `alpine:3.22` | PASS (10/10) |
| V-GITEA | P0 | `gitea/gitea:1.24-rootless` | PASS (9/9) |

## Failures

- H-MINIO / host-candidate: BLOCKED minio/minio:latest cannot be pulled (Error response from daemon: pull access denied for minio/minio, repository does not exist or may require authentication: unauthorized: authentication required)
- H-SEAWEEDFS / host-candidate: FAIL docker stop ends the server (exit 137)
