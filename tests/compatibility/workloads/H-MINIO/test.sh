# H-MINIO: MinIO object storage. Docker Hub answers HTTP 401 for minio/minio
# (MinIO stopped publishing community images in 2025), so it cannot be
# pulled; recorded as BLOCKED, not replaced. H-SEAWEEDFS is a separate test.
# timeout: 60
if dk pull -q minio/minio:latest >/dev/null 2>&1; then
    echo "NOTE minio/minio:latest pulled after all: write a real test"
    ok 1 "MinIO test not written"
else
    echo "BLOCKED minio/minio:latest cannot be pulled ($(dk pull minio/minio:latest 2>&1 | tail -1))"
fi
